package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type forwardBody struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Family  string `json:"family"`
}

func publish(t *testing.T, a *testApp, instanceID, family string, port int) (int, forwardBody) {
	t.Helper()

	body := `{"instance_id":"` + instanceID + `","target_port":80,"node_port":` +
		strconv.Itoa(port)
	if family != "" {
		body += `,"family":"` + family + `"`
	}
	body += `}`

	rec := do(t, a, http.MethodPost, "/v1/forwards", strings.NewReader(body))

	var got forwardBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func TestAForwardPublishesTheFirstAddressByDefault(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.80.0.0/16", "fd00:e0:1::/48")

	id := createOnNetworks(t, a, "web", network)
	waitForNICs(t, a, nodeID, id, 1)

	code, got := publish(t, a, id, "", 8080)
	if code != http.StatusCreated {
		t.Fatalf("publish: %d", code)
	}
	if !strings.HasPrefix(got.Address, "10.80.") {
		t.Fatalf("address = %q, want the first range with no family asked for", got.Address)
	}
}

func TestAForwardCanPublishTheSecondAddress(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.81.0.0/16", "fd00:e0:2::/48")

	id := createOnNetworks(t, a, "web", network)
	waitForNICs(t, a, nodeID, id, 1)

	code, got := publish(t, a, id, "ipv6", 8081)
	if code != http.StatusCreated {
		t.Fatalf("publish: %d", code)
	}
	if !strings.HasPrefix(got.Address, "fd00:e0:2:") {
		t.Fatalf("address = %q, want the v6 address the instance also holds", got.Address)
	}
	if got.Family != "ipv6" {
		t.Fatalf("family = %q, want it recorded so a reader knows which side is published",
			got.Family)
	}
}

func TestPublishingAFamilyTheInstanceDoesNotHaveIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "plain", "10.82.0.0/16")

	id := createOnNetworks(t, a, "web", network)
	waitForNICs(t, a, nodeID, id, 1)

	code, _ := publish(t, a, id, "ipv6", 8082)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: publishing an address that does not exist would "+
			"render a rule pointing nowhere", code, http.StatusConflict)
	}
}

func TestAnUnknownFamilyIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.83.0.0/16", "fd00:e0:3::/48")

	id := createOnNetworks(t, a, "web", network)
	waitForNICs(t, a, nodeID, id, 1)

	if code, _ := publish(t, a, id, "ipv7", 8083); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", code, http.StatusBadRequest)
	}
}

func TestABalancerBalancesTheFamilyItWasGiven(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newDualNetwork(t, a, "dual", "10.84.0.0/16", "fd00:e0:4::/48")

	first := createOnNetworks(t, a, "web-1", network)
	second := createOnNetworks(t, a, "web-2", network)
	waitForNICs(t, a, nodeID, first, 1)
	waitForNICs(t, a, nodeID, second, 1)

	body := `{"name":"six","target_port":80,"listen_port":8443,"family":"ipv6",` +
		`"instances":["` + first + `","` + second + `"]}`
	rec := do(t, a, http.MethodPost, "/v1/balancers", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Family   string `json:"family"`
		Backends []struct {
			Address string `json:"address"`
		} `json:"backends"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if created.Family != "ipv6" {
		t.Fatalf("family = %q", created.Family)
	}
	if len(created.Backends) != 2 {
		t.Fatalf("backends = %d", len(created.Backends))
	}
	for _, backend := range created.Backends {
		if !strings.Contains(backend.Address, ":") {
			t.Fatalf("backend = %q, want the v6 address: one nftables map holds one address "+
				"type, so a mixed set renders nothing at all", backend.Address)
		}
	}
}
