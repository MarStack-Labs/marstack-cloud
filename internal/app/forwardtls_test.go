package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type fwdCertBody struct {
	ID  string `json:"id"`
	TLS *struct {
		Subject string `json:"subject"`
	} `json:"tls"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

type fwdCertList struct {
	Forwards []fwdCertBody `json:"forwards"`
}

func publishedFor(t *testing.T, a *testApp, nodeID string) string {
	t.Helper()

	network := newNetwork(t, a, "pub", "10.130.0.0/16")
	id := createOnNetworks(t, a, "web", network)
	waitForNICs(t, a, nodeID, id, 1)

	rec := do(t, a, http.MethodPost, "/v1/forwards",
		strings.NewReader(`{"instance_id":"`+id+`","target_port":80,"node_port":9090}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	var created fwdCertBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func attachForwardCert(t *testing.T, a *testApp, id, certPEM, keyPEM string) (int, string) {
	t.Helper()

	body, _ := json.Marshal(map[string]string{
		"certificate": certPEM,
		"private_key": keyPEM,
	})
	rec := do(t, a, http.MethodPut, "/v1/forwards/"+id+"/certificate",
		strings.NewReader(string(body)))
	return rec.Code, rec.Body.String()
}

func TestAPublishedPortSummarisesItsCertificateAndServesItToTheNode(t *testing.T) {
	a, nodeID := newSealingApp(t)
	id := publishedFor(t, a, nodeID)

	certPEM, keyPEM := pemPair(t, "pub.example")
	code, raw := attachForwardCert(t, a, id, certPEM, keyPEM)
	if code != http.StatusOK {
		t.Fatalf("attach: %d %s", code, raw)
	}
	if strings.Contains(raw, "PRIVATE KEY") {
		t.Fatal("the response carried the private key back")
	}

	var got fwdCertBody
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TLS == nil || got.TLS.Subject != "pub.example" {
		t.Fatalf("tls = %+v, want the subject summarised", got.TLS)
	}

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/forwards", nil)
	var list fwdCertList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, f := range list.Forwards {
		if f.ID != id {
			continue
		}
		if f.Certificate != certPEM || f.PrivateKey != keyPEM {
			t.Fatal("the node did not get the material it has to terminate with")
		}
		return
	}
	t.Fatal("the forward is not in the node's view")
}

func TestAForwardPrivateKeyIsNotOnDiskInTheClear(t *testing.T) {
	dir := schemaDir(t)
	a, _ := sealingAppIn(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.scheduler.Run(ctx)

	nodeID := registerNode(t, a, "bm-1", "rack-a")
	id := publishedFor(t, a, nodeID)

	certPEM, keyPEM := pemPair(t, "pub.example")
	if code, raw := attachForwardCert(t, a, id, certPEM, keyPEM); code != http.StatusOK {
		t.Fatalf("attach: %d %s", code, raw)
	}

	onDisk := everythingUnder(t, dir)
	if strings.Contains(onDisk, "PRIVATE KEY") {
		t.Fatal("a private key sits in the control plane's data directory in the clear")
	}
	if !strings.Contains(onDisk, "pub.example") {
		t.Fatal("the subject is not stored, so listing forwards would need the key")
	}
}

func TestAMismatchedForwardPairIsRefused(t *testing.T) {
	a, nodeID := newSealingApp(t)
	id := publishedFor(t, a, nodeID)

	certPEM, _ := pemPair(t, "pub.example")
	_, otherKey := pemPair(t, "other.example")

	if code, _ := attachForwardCert(t, a, id, certPEM, otherKey); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a key that does not match must fail when it is "+
			"attached, not when a connection arrives", code, http.StatusBadRequest)
	}
}

func TestAForwardCertificateIsRefusedWithNoKeyToSealItWith(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := publishedFor(t, a, nodeID)

	certPEM, keyPEM := pemPair(t, "pub.example")
	if code, _ := attachForwardCert(t, a, id, certPEM, keyPEM); code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", code, http.StatusConflict)
	}
}

func TestRemovingAForwardCertificateTakesItBackToPlain(t *testing.T) {
	a, nodeID := newSealingApp(t)
	id := publishedFor(t, a, nodeID)

	certPEM, keyPEM := pemPair(t, "pub.example")
	attachForwardCert(t, a, id, certPEM, keyPEM)

	rec := do(t, a, http.MethodDelete, "/v1/forwards/"+id+"/certificate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}

	nodeRec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/forwards", nil)
	if strings.Contains(nodeRec.Body.String(), "PRIVATE KEY") {
		t.Fatal("the node still gets a key for a forward that no longer terminates")
	}
}
