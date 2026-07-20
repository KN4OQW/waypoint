package peering

import (
	"context"
	"testing"
	"time"
)

// fakeDiscovery is the in-memory Discovery the tests use so the pairing logic runs
// without multicast (the interface exists exactly for this).
type fakeDiscovery struct {
	advertised map[string]int
	peers      []Found
}

func (f *fakeDiscovery) Advertise(instance string, port int, _ []string) (func(), error) {
	if f.advertised == nil {
		f.advertised = map[string]int{}
	}
	f.advertised[instance] = port
	return func() { delete(f.advertised, instance) }, nil
}

func (f *fakeDiscovery) Browse(_ context.Context, _ time.Duration) ([]Found, error) {
	return f.peers, nil
}

func TestDiscoveryInterfaceFake(t *testing.T) {
	f := &fakeDiscovery{peers: []Found{{Instance: "garage", Host: "10.0.0.20", Port: 42500}}}
	stop, err := f.Advertise("shack", 42500, []string{"fingerprint=AB:CD"})
	if err != nil {
		t.Fatal(err)
	}
	if f.advertised["shack"] != 42500 {
		t.Fatal("advertise should record the instance+port")
	}
	got, _ := f.Browse(context.Background(), time.Second)
	if len(got) != 1 || got[0].Instance != "garage" || got[0].Host != "10.0.0.20" {
		t.Fatalf("browse returned %+v", got)
	}
	stop()
	if _, ok := f.advertised["shack"]; ok {
		t.Fatal("stop should withdraw the advertisement")
	}
	// a Manager accepts the interface, so real mDNS is a drop-in with no logic change
	var _ Discovery = f
	var _ Discovery = NewMDNS()
}
