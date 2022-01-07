// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package gnmit is a single-target gNMI collector implementation that can be
// used as an on-device/fake device implementation. It supports the Subscribe RPC
// using the libraries from openconfig/gnmi.
package gnmit

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/openconfig/gnmi/cache"
	"github.com/openconfig/gnmi/subscribe"
	"github.com/openconfig/ygot/ygot"
	"github.com/openconfig/ygot/ytypes"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

var (
	// metadataUpdatePeriod is the period of time after which the metadata for the collector
	// is updated to the client.
	metadataUpdatePeriod = time.Duration(30 * time.Second)
	// sizeUpdatePeriod is the period of time after which the storage size information for
	// the collector is updated to the client.
	sizeUpdatePeriod = time.Duration(30 * time.Second)
)

// periodic runs the function fn every period.
func periodic(period time.Duration, fn func()) {
	if period == 0 {
		return
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for range t.C {
		fn()
	}
}

// GNMIServer implements the gNMI server interface.
type GNMIServer struct {
	// The subscribe Server implements only Subscribe for gNMI.
	*subscribe.Server
	c *Collector
}

// New returns a new collector that listens on the specified addr (in the form host:port),
// supporting a single downstream target named hostname. sendMeta controls whether the
// metadata *other* than meta/sync and meta/connected is sent by the collector.
//
// New returns the new collector, the address it is listening on in the form hostname:port
// or any errors encounted whilst setting it up.
func New(ctx context.Context, addr string, hostname string, sendMeta bool, opts ...grpc.ServerOption) (*Collector, string, error) {
	c := &Collector{
		inCh: make(chan *gpb.SubscribeResponse),
		name: hostname,
	}

	srv := grpc.NewServer(opts...)
	c.cache = cache.New([]string{hostname})
	t := c.cache.GetTarget(hostname)

	if sendMeta {
		go periodic(metadataUpdatePeriod, c.cache.UpdateMetadata)
		go periodic(sizeUpdatePeriod, c.cache.UpdateSize)
	}
	t.Connect()

	// start our single collector from the input channel.
	go func() {
		for {
			select {
			case msg := <-c.inCh:
				if err := c.handleUpdate(msg); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	subscribeSrv, err := subscribe.NewServer(c.cache)
	if err != nil {
		return nil, "", fmt.Errorf("could not instantiate gNMI server: %v", err)
	}

	gnmiserver := &GNMIServer{
		Server: subscribeSrv, // use the 'subscribe' implementation.
		c:      c,
	}

	gpb.RegisterGNMIServer(srv, gnmiserver)
	// Forward streaming updates to clients.
	c.cache.SetClient(subscribeSrv.Update)
	// Register listening port and start serving.
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("failed to listen: %v", err)
	}

	go srv.Serve(lis)
	c.stopFn = srv.GracefulStop
	return c, lis.Addr().String(), nil
}

type populateDefaultser interface {
	PopulateDefaults()
}

// New returns a new collector that listens on the specified addr (in the form host:port),
// supporting a single downstream target named hostname. sendMeta controls whether the
// metadata *other* than meta/sync and meta/connected is sent by the collector.
//
// New returns the new collector, the address it is listening on in the form hostname:port
// or any errors encounted whilst setting it up.
//
// NewSettable is different from New in that the returned collector is
// schema-aware and supports gNMI Set. Currently it is not possible to change
// the schema of a Collector after it is created.
func NewSettable(ctx context.Context, addr string, hostname string, sendMeta bool, schema *ytypes.Schema, opts ...grpc.ServerOption) (*Collector, string, error) {
	if !schema.IsValid() {
		return nil, "", fmt.Errorf("cannot obtain valid schema for GoStructs: %v", schema)
	}
	// Initialize the root with default values.
	schema.Root.(populateDefaultser).PopulateDefaults()
	if err := schema.Root.Validate(); err != nil {
		return nil, "", fmt.Errorf("default root of input schema fails validation: %v", err)
	}

	// FIXME(wenbli): initialize the collector with default values.
	collector, addr, err := New(ctx, addr, hostname, sendMeta, opts...)
	if err != nil {
		return nil, "", err
	}
	collector.schema = schema
	return collector, addr, nil
}

// Stop halts the running collector.
func (c *Collector) Stop() {
	c.stopFn()
}

// handleUpdate handles an input gNMI SubscribeResponse that is received by
// the target.
func (c *Collector) handleUpdate(resp *gpb.SubscribeResponse) error {
	t := c.cache.GetTarget(c.name)
	switch v := resp.Response.(type) {
	case *gpb.SubscribeResponse_Update:
		t.GnmiUpdate(v.Update)
	case *gpb.SubscribeResponse_SyncResponse:
		t.Sync()
	case *gpb.SubscribeResponse_Error:
		return fmt.Errorf("error in response: %s", v)
	default:
		return fmt.Errorf("unknown response %T: %s", v, v)
	}
	return nil
}

// Collector is a basic gNMI target that supports only the Subscribe
// RPC, and acts as a cache for exactly one target.
type Collector struct {
	cache  *cache.Cache
	smu    sync.Mutex
	schema *ytypes.Schema
	// name is the hostname of the client.
	name string
	// inCh is a channel use to write new SubscribeResponses to the client.
	inCh chan *gpb.SubscribeResponse
	// stopFn is the function used to stop the server.
	stopFn func()
}

// TargetUpdate provides an input gNMI SubscribeResponse to update the
// cache and clients with.
func (c *Collector) TargetUpdate(m *gpb.SubscribeResponse) {
	c.inCh <- m
}

func (s *GNMIServer) Set(ctx context.Context, req *gpb.SetRequest) (*gpb.SetResponse, error) {
	if s.c.schema == nil {
		return s.UnimplementedGNMIServer.Set(ctx, req)
	}
	// Create a copy so that we can rollback the transaction when validation fails.
	dirtyRootG, err := ygot.DeepCopy(s.c.schema.Root)
	if err != nil {
		return nil, fmt.Errorf("gnmit: failed to ygot.DeepCopy the cached root object: %v", err)
	}
	dirtyRoot, ok := dirtyRootG.(ygot.ValidatedGoStruct)
	if !ok {
		return nil, fmt.Errorf("gnmit: cannot convert root object to ValidatedGoStruct")
	}
	// Operate at the prefix level.
	nodeI, _, err := ytypes.GetOrCreateNode(s.c.schema.RootSchema(), dirtyRoot, req.Prefix, &ytypes.PreferShadowPath{})
	if err != nil {
		return nil, fmt.Errorf("gnmit: failed to GetOrCreate the prefix node: %v", err)
	}
	node, ok := nodeI.(ygot.GoStruct)
	if !ok {
		return nil, fmt.Errorf("gnmit: prefix path points to a non-GoStruct, this is not allowed: %T, %v", nodeI, nodeI)
	}
	nodeName := reflect.TypeOf(nodeI).Elem().Name()

	// TODO(wenbli): Reject paths that try to modify read-only values.
	// TODO(wenbli): Question: what to do if there are operational-state values in a container that is specified to be replaced or deleted?

	// Process deletes first.
	for _, path := range req.Delete {
		if err := ytypes.DeleteNode(s.c.schema.SchemaTree[nodeName], node, path, &ytypes.PreferShadowPath{}); err != nil {
			return nil, fmt.Errorf("gnmit: DeleteNode error: %v", err)
		}
	}
	for _, update := range req.Replace {
		if err := ytypes.DeleteNode(s.c.schema.SchemaTree[nodeName], node, update.Path, &ytypes.PreferShadowPath{}); err != nil {
			return nil, fmt.Errorf("gnmit: DeleteNode error: %v", err)
		}
		_, _, err := ytypes.GetOrCreateNode(s.c.schema.SchemaTree[nodeName], node, update.Path, &ytypes.PreferShadowPath{})
		if err != nil {
			return nil, fmt.Errorf("gnmit: failed to GetOrCreate a replace node: %v", err)
		}
		// TODO(wenbli): Populate default values using PopulateDefaults.
		if err := ytypes.SetNode(s.c.schema.SchemaTree[nodeName], node, update.Path, update.Val, &ytypes.PreferShadowPath{}); err != nil {
			return nil, fmt.Errorf("gnmit: SetNode failed on leaf node: %v", err)
		}
	}
	for _, update := range req.Update {
		_, _, err := ytypes.GetOrCreateNode(s.c.schema.SchemaTree[nodeName], node, update.Path, &ytypes.PreferShadowPath{})
		if err != nil {
			return nil, fmt.Errorf("gnmit: failed to GetOrCreate a replace node: %v", err)
		}
		if err := ytypes.SetNode(s.c.schema.SchemaTree[nodeName], node, update.Path, update.Val, &ytypes.PreferShadowPath{}); err != nil {
			return nil, fmt.Errorf("gnmit: SetNode failed on leaf node: %v", err)
		}
	}

	if err := dirtyRoot.Validate(); err != nil {
		return nil, fmt.Errorf("gnmit: invalid SetRequest: %v", err)
	}

	n, err := ygot.Diff(s.c.schema.Root, dirtyRoot)
	if err != nil {
		return nil, fmt.Errorf("gnmit: error while creating update notification for Set: %v", err)
	}
	n.Timestamp = time.Now().UnixNano()
	n.Prefix = &gpb.Path{Origin: req.Prefix.Origin, Target: s.c.name}
	// XXX(wenbli): The following delineated code only works with Ondatra, whose generated GoStructs prefer operational state.
	// ---------------------------------
	var deletes []*gpb.Path
	for _, path := range n.Delete {
		if path.Elem[len(path.Elem)-2].Name != "state" {
			return nil, fmt.Errorf("gnmit: Unexpected non-state value for deletion. Currently gnmit's Set only supports Ondatra.")
		}
		configPath := proto.Clone(path).(*gpb.Path)
		configPath.Elem[len(configPath.Elem)-2].Name = "config"
		deletes = append(deletes, configPath)
	}
	n.Delete = append(deletes, n.Delete...)
	var updates []*gpb.Update
	for _, update := range n.Update {
		if update.Path.Elem[len(update.Path.Elem)-2].Name != "state" {
			return nil, fmt.Errorf("gnmit: Unexpected non-state value for update. Currently gnmit's Set only supports Ondatra.")
		}
		configPath := proto.Clone(update.Path).(*gpb.Path)
		configPath.Elem[len(configPath.Elem)-2].Name = "config"
		updates = append(updates, &gpb.Update{Path: configPath, Val: update.Val})
	}
	n.Update = append(updates, n.Update...)
	// ---------------------------------

	// Update cache
	t := s.c.cache.GetTarget(s.c.name)
	if err := t.GnmiUpdate(n); err != nil {
		return nil, err
	}
	// TODO(wenbli): Should handle updates one at a time to avoid partial updates being reflected in the cache when an error occurs. AKA we should support transactional semantics.
	s.c.schema.Root = dirtyRoot
	// TODO(wenbli): Currently the SetResponse is not filled.
	return &gpb.SetResponse{}, nil
}
