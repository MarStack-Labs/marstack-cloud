package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type dualNIC struct {
	InstanceID string `json:"instance_id"`
	Device     int    `json:"device"`
	IP         string `json:"ip"`
	IP6        string `json:"ip6"`
}

type dualNetworks struct {
	Networks []struct {
		Name     string    `json:"name"`
		CIDR     string    `json:"cidr"`
		CIDR6    string    `json:"cidr6"`
		Gateway6 string    `json:"gateway6"`
		Slice    string    `json:"slice"`
		Slice6   string    `json:"slice6"`
		NICs     []dualNIC `json:"nics"`
	} `json:"networks"`
}

func newDualNetwork(t *testing.T, a *testApp, name, cidr, cidr6 string) string {
	t.Helper()

	body := `{"name":"` + name + `","cidr":"` + cidr + `","cidr6":"` + cidr6 + `"}`
	rec := do(t, a, http.MethodPost, "/v1/networks", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create network: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID       string `json:"id"`
		CIDR6    string `json:"cidr6"`
		Gateway6 string `json:"gateway6"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.CIDR6 == "" || created.Gateway6 == "" {
		t.Fatalf("the second range was dropped: %s", rec.Body.String())
	}
	return created.ID
}

func dualView(t *testing.T, a *testApp, nodeID, instanceID string) dualNIC {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/network", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node view: %d %s", rec.Code, rec.Body.String())
	}

	var body dualNetworks
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, n := range body.Networks {
		for _, nic := range n.NICs {
			if nic.InstanceID == instanceID {
				return nic
			}
		}
	}
	return dualNIC{}
}

func TestOneInterfaceCarriesAnAddressFromEachFamily(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.70.0.0/16", "fd00:d0:1::/48")

	id := createOnNetworks(t, a, "both", network)
	waitForNICs(t, a, nodeID, id, 1)

	nic := dualView(t, a, nodeID, id)
	if !strings.HasPrefix(nic.IP, "10.70.") {
		t.Fatalf("ip = %q, want one out of the first range", nic.IP)
	}
	if !strings.HasPrefix(nic.IP6, "fd00:d0:1:") {
		t.Fatalf("ip6 = %q, want one out of the second range on the same interface", nic.IP6)
	}
	if nic.Device != 0 {
		t.Fatalf("device = %d, want both addresses on one interface rather than two", nic.Device)
	}
}

func TestASingleFamilyNetworkStillGetsOneAddress(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "plain", "10.71.0.0/16")

	id := createOnNetworks(t, a, "single", network)
	waitForNICs(t, a, nodeID, id, 1)

	nic := dualView(t, a, nodeID, id)
	if nic.IP == "" {
		t.Fatal("the instance got no address at all")
	}
	if nic.IP6 != "" {
		t.Fatalf("ip6 = %q, want nothing: the network has no second range", nic.IP6)
	}
}

func TestBothSlicesAreHandedToTheNode(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.72.0.0/16", "fd00:d0:2::/48")

	id := createOnNetworks(t, a, "both", network)
	waitForNICs(t, a, nodeID, id, 1)

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/network", nil)
	var body dualNetworks
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, n := range body.Networks {
		if n.Name != "dual" {
			continue
		}
		if !strings.HasPrefix(n.Slice, "10.72.") {
			t.Fatalf("slice = %q", n.Slice)
		}
		if !strings.HasSuffix(n.Slice6, "/64") {
			t.Fatalf("slice6 = %q, want a /64 the node can route to", n.Slice6)
		}
		if n.Gateway6 == "" {
			t.Fatal("the node was told no second gateway, so it cannot add the route")
		}
		return
	}
	t.Fatalf("the network is not in the node's view: %s", rec.Body.String())
	_ = id
}

func TestBothFamiliesAppearInDNSForOneName(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.73.0.0/16", "fd00:d0:3::/48")

	id := createOnNetworks(t, a, "both", network)
	waitForNICs(t, a, nodeID, id, 1)

	rec := do(t, a, http.MethodGet, "/v1/dns/records", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("records: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Records []struct {
			FQDN string `json:"fqdn"`
			IP   string `json:"ip"`
		} `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var four, six int
	for _, r := range body.Records {
		if r.FQDN != "both.dual.internal" {
			continue
		}
		if strings.Contains(r.IP, ":") {
			six++
		} else {
			four++
		}
	}

	if four != 1 || six != 1 {
		t.Fatalf("records for one name: %d v4 and %d v6, want one of each so an A query and "+
			"an AAAA query both get an answer", four, six)
	}
}

func TestASecondRangeOfTheSameFamilyIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	for _, body := range []string{
		`{"name":"same","cidr":"10.74.0.0/16","cidr6":"10.75.0.0/16"}`,
		`{"name":"same","cidr":"fd00:d0:4::/48","cidr6":"fd00:d0:5::/48"}`,
		`{"name":"same","cidr":"10.74.0.0/16","cidr6":"2001:db8::/48"}`,
	} {
		rec := do(t, a, http.MethodPost, "/v1/networks", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestASecondRangeCannotOverlapAnotherNetwork(t *testing.T) {
	a, _ := newBalancingApp(t)
	newDualNetwork(t, a, "first", "10.76.0.0/16", "fd00:d0:6::/48")

	body := `{"name":"second","cidr":"10.77.0.0/16","cidr6":"fd00:d0:6::/48"}`
	rec := do(t, a, http.MethodPost, "/v1/networks", strings.NewReader(body))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: every node routes to a peer slice by its prefix, "+
			"so two networks cannot share one in either family", rec.Code, http.StatusConflict)
	}
}
