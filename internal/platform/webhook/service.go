package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Events interface {
	Since(ctx context.Context, afterID int64, limit int) ([]Event, error)
	NewestID(ctx context.Context) (int64, error)
}

type clock func() time.Time

type service struct {
	repo   *repository
	events Events
	http   *http.Client
	log    *slog.Logger
	now    clock
}

func newService(repo *repository, log *slog.Logger, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, http: newClient(nil), log: log, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Subscription, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Subscription{}, err
	}

	target, err := checkURL(params.URL)
	if err != nil {
		return Subscription{}, fault.Invalid("invalid_url", err.Error())
	}

	kinds, err := checkKinds(params.Kinds)
	if err != nil {
		return Subscription{}, err
	}

	secret, err := newSecret()
	if err != nil {
		return Subscription{}, fault.Internal(err)
	}

	sub := Subscription{
		ID:        ids.New("wh"),
		ProjectID: params.ProjectID,
		Name:      params.Name,
		URL:       target,
		Secret:    secret,
		Kinds:     kinds,
		Active:    true,
		CreatedAt: s.now(),
	}

	if err := s.repo.insert(ctx, sub); err != nil {
		return Subscription{}, translate(err)
	}
	return sub, nil
}

func checkKinds(kinds []string) ([]string, error) {
	if len(kinds) > MaxKinds {
		return nil, fault.Invalid("too_many_kinds", fmt.Sprintf(
			"a webhook filters on at most %d kinds", MaxKinds))
	}

	cleaned := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if len(kind) > MaxKindLength {
			return nil, fault.Invalid("invalid_kind", "a kind is at most "+
				strconv.Itoa(MaxKindLength)+" characters")
		}
		if strings.ContainsAny(kind, " \t\r\n") {
			return nil, fault.Invalid("invalid_kind", "a kind carries no whitespace")
		}
		if strings.Contains(kind, "*") && !strings.HasSuffix(kind, ".*") {
			return nil, fault.Invalid("invalid_kind",
				"the only wildcard is a trailing .* such as instance.*")
		}
		cleaned = append(cleaned, kind)
	}
	return cleaned, nil
}

func newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a webhook secret: %w", err)
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func matches(kinds []string, kind string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, wanted := range kinds {
		if wanted == kind {
			return true
		}
		if strings.HasSuffix(wanted, ".*") &&
			strings.HasPrefix(kind, strings.TrimSuffix(wanted, "*")) {
			return true
		}
	}
	return false
}

func (s *service) find(ctx context.Context, projectID, id string) (Subscription, error) {
	sub, err := s.repo.byID(ctx, projectID, id)
	if errors.Is(err, errNotFound) {
		sub, err = s.repo.byName(ctx, projectID, id)
	}
	if err != nil {
		return Subscription{}, translate(err)
	}
	return sub, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Subscription, error) {
	subs, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return subs, nil
}

func (s *service) setActive(ctx context.Context, projectID, id string, active bool) (Subscription, error) {
	sub, err := s.find(ctx, projectID, id)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.repo.setActive(ctx, sub.ID, active); err != nil {
		return Subscription{}, translate(err)
	}
	return s.find(ctx, projectID, sub.ID)
}

func (s *service) remove(ctx context.Context, projectID, id string) error {
	sub, err := s.find(ctx, projectID, id)
	if err != nil {
		return err
	}
	if err := s.repo.delete(ctx, sub.ID); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) deliveries(ctx context.Context, projectID, id string, limit int) ([]Delivery, error) {
	sub, err := s.find(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxBatch {
		limit = MaxBatch
	}

	found, err := s.repo.deliveriesOf(ctx, sub.ID, limit)
	if err != nil {
		return nil, translate(err)
	}
	return found, nil
}

func (s *service) fanOut(ctx context.Context) (int, error) {
	if s.events == nil {
		return 0, nil
	}

	at, err := s.repo.cursor(ctx)
	if err != nil {
		return 0, translate(err)
	}
	if at < 0 {
		newest, err := s.events.NewestID(ctx)
		if err != nil {
			return 0, err
		}
		if err := s.repo.setCursor(ctx, newest); err != nil {
			return 0, translate(err)
		}
		return 0, nil
	}

	subs, err := s.repo.active(ctx)
	if err != nil {
		return 0, translate(err)
	}

	entries, err := s.events.Since(ctx, at, MaxBatch)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	now := s.now()
	queued := make([]Delivery, 0, len(entries))
	highest := at

	for _, entry := range entries {
		if entry.ID > highest {
			highest = entry.ID
		}
		for _, sub := range subs {
			if sub.ProjectID != entry.ProjectID || !matches(sub.Kinds, entry.Kind) {
				continue
			}
			queued = append(queued, Delivery{
				ID:             ids.New("whd"),
				SubscriptionID: sub.ID,
				EventID:        entry.ID,
				Kind:           entry.Kind,
				Subject:        entry.Subject,
				State:          StatePending,
				NextAttemptAt:  now,
				CreatedAt:      now,
				UpdatedAt:      now,
			})
		}
	}

	if err := s.repo.enqueue(ctx, queued, highest); err != nil {
		return 0, translate(err)
	}
	return len(queued), nil
}

func (s *service) drain(ctx context.Context) (int, error) {
	now := s.now()

	due, err := s.repo.due(ctx, now, MaxDueBatch)
	if err != nil {
		return 0, translate(err)
	}
	if len(due) == 0 {
		return 0, nil
	}

	byID := map[string]Subscription{}
	subs, err := s.repo.active(ctx)
	if err != nil {
		return 0, translate(err)
	}
	for _, sub := range subs {
		byID[sub.ID] = sub
	}

	sent := 0
	for _, delivery := range due {
		sub, live := byID[delivery.SubscriptionID]
		if !live {
			delivery.State = StateFailed
			delivery.LastError = "the webhook is paused or gone"
			delivery.UpdatedAt = now
			if err := s.repo.markDelivery(ctx, delivery); err != nil {
				s.log.Warn("could not mark a delivery", "delivery", delivery.ID, "error", err)
			}
			continue
		}

		if err := s.post(ctx, sub, delivery); err != nil {
			s.retry(ctx, delivery, err, now)
			continue
		}

		delivery.State = StateDelivered
		delivery.LastError = ""
		delivery.Attempts++
		delivery.UpdatedAt = now
		if err := s.repo.markDelivery(ctx, delivery); err != nil {
			s.log.Warn("could not mark a delivery", "delivery", delivery.ID, "error", err)
			continue
		}
		sent++
	}

	if err := s.repo.pruneDeliveries(ctx, RetainFailed); err != nil {
		s.log.Warn("could not prune deliveries", "error", err)
	}
	return sent, nil
}

func (s *service) retry(ctx context.Context, delivery Delivery, cause error, now time.Time) {
	delivery.Attempts++
	delivery.LastError = clip(cause.Error(), MaxURLLength)
	delivery.UpdatedAt = now

	if delivery.Attempts >= MaxAttempts {
		delivery.State = StateFailed
		s.log.Warn("gave up on a webhook delivery",
			"delivery", delivery.ID, "attempts", delivery.Attempts, "error", cause)
	} else {
		delivery.NextAttemptAt = now.Add(backoffFor(delivery.Attempts))
	}

	if err := s.repo.markDelivery(ctx, delivery); err != nil {
		s.log.Warn("could not mark a delivery", "delivery", delivery.ID, "error", err)
	}
}

func backoffFor(attempts int) time.Duration {
	wait := FirstBackoff
	for range attempts - 1 {
		wait *= 2
		if wait >= MaxBackoff {
			return MaxBackoff
		}
	}
	return wait
}

type payload struct {
	Delivery string `json:"delivery"`
	EventID  int64  `json:"event_id"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject,omitempty"`
	Attempt  int    `json:"attempt"`
}

func (s *service) post(ctx context.Context, sub Subscription, delivery Delivery) error {
	body, err := json.Marshal(payload{
		Delivery: delivery.ID,
		EventID:  delivery.EventID,
		Kind:     delivery.Kind,
		Subject:  delivery.Subject,
		Attempt:  delivery.Attempts + 1,
	})
	if err != nil {
		return fmt.Errorf("encode the payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventHeader, delivery.Kind)
	req.Header.Set(SignatureHeader, sign(sub.Secret, body))

	res, err := s.http.Do(req)
	if err != nil {
		return errors.New(reason(err))
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4*1024))

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return errors.New("the target answered " + strconv.Itoa(res.StatusCode))
	}
	return nil
}

func reason(err error) string {
	var wrapped *url.Error
	if errors.As(err, &wrapped) && wrapped.Err != nil {
		return wrapped.Err.Error()
	}
	return err.Error()
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("webhook_not_found", "no webhook with that name or id exists")
	case errors.Is(err, errNameUsed):
		return fault.Conflict("webhook_name_taken",
			"a webhook with that name already exists in this project")
	default:
		return err
	}
}
