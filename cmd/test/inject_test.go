package inject

import (
	"context"
	"flag"
	"testing"

	log "github.com/golang/glog"

	"github.com/openconfig/gribigo/compliance"
	"github.com/openconfig/gribigo/fluent"
	"github.com/openconfig/gribigo/server"
	"google.golang.org/grpc"

	spb "github.com/openconfig/gribi/v1/proto/service"
)

var (
	addr              = flag.String("addr", "", "address of the gRIBI server in the format hostname:port")
	insecure          = flag.Bool("insecure", false, "dial insecure gRPC (no TLS)")
	initialElectionID = flag.Uint("initial_electionid", 0, "initial election ID to be used")
)

func TestInject(t *testing.T) {
	if *addr == "" {
		log.Errorf("Must specify gRIBI server address, got: %v", *addr)
		return // Test is part of CI, so do not fail here.
	}

	if *initialElectionID != 0 {
		compliance.SetElectionID(uint64(*initialElectionID))
	}

	compliance.SetDefaultNetworkInstanceName(server.DefaultNetworkInstanceName)

	dialOpts := []grpc.DialOption{grpc.WithBlock()}
	dialOpts = append(dialOpts, grpc.WithInsecure())

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, *addr, dialOpts...)
	if err != nil {
		t.Fatalf("Could not dial gRPC: %v", err)
	}
	defer conn.Close()
	stub := spb.NewGRIBIClient(conn)

	c := fluent.NewClient()
	c.Connection().WithStub(stub)

	compliance.AddIPv4Entry(c, fluent.InstalledInRIB, t)
}
