module github.com/openconfig/gribigo

go 1.16

require (
	github.com/golang/glog v1.0.0
	github.com/google/go-cmp v0.5.7
	github.com/google/uuid v1.1.2
	github.com/kentik/patricia v1.2.0
	github.com/openconfig/gnmi v0.0.0-20220617175856-41246b1b3507
	github.com/openconfig/goyang v1.1.0
	github.com/openconfig/gribi v0.1.1-0.20220622162620-08d53dffce45
	github.com/openconfig/lemming v0.0.0-20220621231916-cd364cd4edd6
	github.com/openconfig/testt v0.0.0-20220311054427-efbb1a32ec07
	github.com/openconfig/ygot v0.24.2
	go.uber.org/atomic v1.7.0
	google.golang.org/genproto v0.0.0-20211203200212-54befc351ae9
	google.golang.org/grpc v1.42.0
	google.golang.org/protobuf v1.28.0
	lukechampine.com/uint128 v1.1.1
)

replace github.com/openconfig/lemming => /usr/local/google/home/wenbli/gocode/src/github.com/openconfig/lemming
