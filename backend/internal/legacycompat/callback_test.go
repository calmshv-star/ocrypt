package legacycompat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/webhook"
)

type callbackResolverFake struct{}

func (callbackResolverFake) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("93.184.216.34")}, nil
}

func TestLegacyAcknowledgementIsExactLowercase(t *testing.T) {
	for _, body := range []string{"ok", " ok\n", "success"} {
		if !isLegacyAck([]byte(body)) {
			t.Fatalf("rejected %q", body)
		}
	}
	for _, body := range []string{"OK", "SUCCESS", "okay", "success!", ""} {
		if isLegacyAck([]byte(body)) {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestCallbackRejectsNonstandardHTTPSPort(t *testing.T) {
	job := CallbackJob{TargetURL: "https://merchant.example:8443/callback", HTTPMethod: "POST", FrozenBody: []byte("{}")}
	if _, _, err := (CallbackSender{Resolver: callbackResolverFake{}}).Send(context.Background(), job); err == nil {
		t.Fatal("accepted callback port other than 443")
	}
}

type callbackTransportFake struct {
	status int
	body   []byte
	err    error
}

type lostAckTransport struct {
	bodies [][]byte
}

func (transport *lostAckTransport) Send(_ context.Context, job CallbackJob) (int, [32]byte, error) {
	transport.bodies = append(transport.bodies, append([]byte(nil), job.FrozenBody...))
	if len(transport.bodies) == 1 {
		return 0, [32]byte{}, errors.New("lost acknowledgement")
	}
	return http.StatusOK, sha256.Sum256([]byte("ok")), nil
}

func (fake callbackTransportFake) Send(context.Context, CallbackJob) (int, [32]byte, error) {
	return fake.status, sha256.Sum256(fake.body), fake.err
}

type deliveryRepository struct {
	*memoryRepository
	job       CallbackJob
	ackErr    error
	acked     int
	failed    int
	failedJob CallbackJob
}

func (repository *deliveryRepository) ClaimCallbacks(context.Context, string, int, time.Duration, time.Time) ([]CallbackJob, error) {
	return []CallbackJob{repository.job}, nil
}
func (repository *deliveryRepository) AcknowledgeCallback(context.Context, string, string, int64, int, [32]byte, time.Time) (bool, error) {
	repository.acked++
	return repository.ackErr == nil, repository.ackErr
}
func (repository *deliveryRepository) FailCallback(_ context.Context, id, token string, fence int64, _ string, _ int, _ time.Time) (bool, error) {
	repository.failed++
	repository.failedJob = repository.job
	return id == repository.job.DeliveryID && token == repository.job.LeaseToken && fence == repository.job.Fence, nil
}

func TestCallbackPostAckDatabaseFailureRemainsRecoverable(t *testing.T) {
	repository := &deliveryRepository{memoryRepository: &memoryRepository{}, job: CallbackJob{DeliveryID: "delivery", LeaseToken: "lease", Fence: 7, FrozenBody: []byte("frozen")}, ackErr: errors.New("database unavailable")}
	service := Service{Repository: repository, Now: func() time.Time { return time.Unix(1800000000, 0) }}
	err := service.Deliver(context.Background(), "worker", 1, 30*time.Second, callbackTransportFake{status: 200, body: []byte("ok")})
	if err == nil || repository.acked != 1 || repository.failed != 0 {
		t.Fatalf("err=%v acked=%d failed=%d", err, repository.acked, repository.failed)
	}
}

func TestCallbackOutageSchedulesFrozenRetry(t *testing.T) {
	job := CallbackJob{DeliveryID: "delivery", LeaseToken: "lease", Fence: 8, FrozenBody: []byte("immutable-payload"), CredentialVersionID: "credential-v1", CallbackKeyID: "key-v1"}
	repository := &deliveryRepository{memoryRepository: &memoryRepository{}, job: job}
	metrics := &Metrics{}
	service := Service{Repository: repository, Metrics: metrics, Now: func() time.Time { return time.Unix(1800000000, 0) }}
	err := service.Deliver(context.Background(), "worker", 1, 30*time.Second, callbackTransportFake{err: errors.New("TLS outage")})
	if err != nil || repository.failed != 1 || string(repository.failedJob.FrozenBody) != "immutable-payload" || repository.failedJob.CredentialVersionID != "credential-v1" || metrics.CallbacksFailed.Load() != 1 {
		t.Fatalf("err=%v failed=%d job=%+v metrics=%d", err, repository.failed, repository.failedJob, metrics.CallbacksFailed.Load())
	}
}

func TestLostCallbackAckRetriesFrozenBytesAndCommitsOnce(t *testing.T) {
	job := CallbackJob{DeliveryID: "delivery", LeaseToken: "lease-1", Fence: 1, FrozenBody: []byte("immutable-payload"), CredentialVersionID: "credential-v1", CallbackKeyID: "key-v1"}
	repository := &deliveryRepository{memoryRepository: &memoryRepository{}, job: job}
	transport := &lostAckTransport{}
	service := Service{Repository: repository, Now: func() time.Time { return time.Unix(1800000000, 0) }}
	if err := service.Deliver(context.Background(), "worker", 1, 30*time.Second, transport); err != nil {
		t.Fatal(err)
	}
	repository.job.LeaseToken = "lease-2"
	repository.job.Fence = 2
	if err := service.Deliver(context.Background(), "worker", 1, 30*time.Second, transport); err != nil {
		t.Fatal(err)
	}
	if repository.failed != 1 || repository.acked != 1 || len(transport.bodies) != 2 || string(transport.bodies[0]) != string(transport.bodies[1]) {
		t.Fatalf("failed=%d acked=%d bodies=%q", repository.failed, repository.acked, transport.bodies)
	}
}

func TestFreezeJSONMD5CallbackGolden(t *testing.T) {
	mapping := Mapping{Protocol: ProtocolJSONMD5, TradeID: "AAAAAAAAAAAAAAAAAAAAAA", OrderID: "order-001", Amount: "499.00", NotifyURL: "https://merchant.example/callback"}
	credential := Credential{PID: "1000", LegacyToken: "USDT", LegacySecretRef: "legacy", CredentialVersionID: "cred", CallbackKeyID: "legacy-v1"}
	route := CoreRoute{DisplayAmount: "6.2300", Address: "TAddress"}
	event := webhook.Event{EventType: "payment.settled", Settlement: &webhook.Settlement{TransactionHash: "0xabc"}}
	frozen, err := FreezeCallback(mapping, credential, route, event, []byte("legacy-secret-0001"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(frozen.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["signature"] != "b7fa6eb693cf6a2d21d8dc6a2e0cf23e" {
		t.Fatalf("signature=%v body=%s", payload["signature"], frozen.Body)
	}
	if !strings.Contains(string(frozen.Body), `"amount":499`) {
		t.Fatalf("amount not numeric: %s", frozen.Body)
	}
}

func TestFreezeLegacyOverpaymentAsSuccessfulImmutableCallback(t *testing.T) {
	mapping := Mapping{Protocol: ProtocolJSONMD5, TradeID: "AAAAAAAAAAAAAAAAAAAAAA", OrderID: "order-overpaid", Amount: "499.00", NotifyURL: "https://merchant.example/callback"}
	credential := Credential{PID: "1000", LegacyToken: "ETH", CredentialVersionID: "cred", CallbackKeyID: "legacy-v1"}
	event := webhook.Event{EventType: "payment.overpaid", Settlement: &webhook.Settlement{TransactionHash: "0xoverpaid"}}
	frozen, err := FreezeCallback(mapping, credential, CoreRoute{DisplayAmount: "0.0031", Address: "0xmerchant"}, event, []byte("legacy-secret-0001"))
	if err != nil || !strings.Contains(string(frozen.Body), `"status":2`) || !strings.Contains(string(frozen.Body), `"block_transaction_id":"0xoverpaid"`) {
		t.Fatalf("overpayment was not frozen as a paid legacy callback: body=%s err=%v", frozen.Body, err)
	}
}

func TestFreezeFormMD5CallbackPinsCredentialVersion(t *testing.T) {
	mapping := Mapping{Protocol: ProtocolFormMD5, TradeID: "AAAAAAAAAAAAAAAAAAAAAA", OrderID: "order", Amount: "1", Name: "VIP", PaymentType: "usdt.tron", NotifyURL: "https://merchant.example/callback"}
	credential := Credential{PID: "1000", CredentialVersionID: "cred-v1", CallbackKeyID: "legacy-v1"}
	frozen, err := FreezeCallback(mapping, credential, CoreRoute{}, webhook.Event{EventType: "payment.settled", Settlement: &webhook.Settlement{}}, []byte("legacy-secret-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if frozen.CredentialVersionID != "cred-v1" || frozen.CallbackKeyID != "legacy-v1" || !strings.Contains(string(frozen.Body), "sign_type=MD5") {
		t.Fatalf("frozen=%+v", frozen)
	}
}
