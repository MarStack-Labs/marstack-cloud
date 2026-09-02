package balancer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var (
	errNotFound = errors.New("balancer not found")
	errPortUsed = errors.New("listen port already claimed")
	errNameUsed = errors.New("balancer name already used")
)

const columns = `id, project_id, name, protocol, listen_port, target_port, algorithm, service_id, check_kind, check_path, rise, fall, family, tls_material, tls_key_id, tls_subject, tls_expires_at, created_at`

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

func (r *repository) insert(ctx context.Context, b Balancer) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO balancers (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ProjectID, b.Name, b.Protocol, b.ListenPort, b.TargetPort, b.Algorithm,
		b.ServiceID, b.Check, b.CheckPath, b.Rise, b.Fall, b.Family,
		b.TLS.Material, b.TLS.KeyID, b.TLS.Subject, b.TLS.ExpiresAt,
		b.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return classify(err, "insert balancer")
	}

	for _, backend := range b.Backends {
		if err := attach(ctx, tx, b.ID, backend.InstanceID, b.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func attach(ctx context.Context, ex execer, balancerID, instanceID string, at time.Time) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO balancer_backends (balancer_id, instance_id, added_at) VALUES (?, ?, ?)`,
		balancerID, instanceID, at.Format(time.RFC3339Nano))
	if err != nil && isUniqueViolation(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("attach a backend: %w", err)
	}
	return nil
}

func (r *repository) addBackend(ctx context.Context, balancerID, instanceID string, at time.Time) error {
	return attach(ctx, r.db, balancerID, instanceID, at)
}

func (r *repository) removeBackend(ctx context.Context, balancerID, instanceID string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM balancer_backends WHERE balancer_id = ? AND instance_id = ?`,
		balancerID, instanceID)
	if err != nil {
		return fmt.Errorf("detach a backend: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("detach a backend: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func (r *repository) countBackends(ctx context.Context, balancerID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM balancer_backends WHERE balancer_id = ?`, balancerID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count backends: %w", err)
	}
	return count, nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Balancer, error) {
	return r.load(ctx,
		`SELECT `+columns+` FROM balancers WHERE project_id = ? ORDER BY listen_port`, projectID)
}

func (r *repository) all(ctx context.Context) ([]Balancer, error) {
	return r.load(ctx, `SELECT `+columns+` FROM balancers ORDER BY listen_port`)
}

func (r *repository) byID(ctx context.Context, projectID, id string) (Balancer, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM balancers WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return Balancer{}, err
	}
	if len(found) == 0 {
		return Balancer{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) byName(ctx context.Context, projectID, name string) (Balancer, error) {
	found, err := r.load(ctx,
		`SELECT `+columns+` FROM balancers WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return Balancer{}, err
	}
	if len(found) == 0 {
		return Balancer{}, errNotFound
	}
	return found[0], nil
}

func (r *repository) portTaken(ctx context.Context, protocol string, port int) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM balancers WHERE protocol = ? AND listen_port = ?`,
		protocol, port).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check the listen port: %w", err)
	}
	return count > 0, nil
}

func (r *repository) load(ctx context.Context, query string, args ...any) ([]Balancer, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list balancers: %w", err)
	}
	defer rows.Close()

	balancers := make([]Balancer, 0, 8)
	for rows.Next() {
		b, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		balancers = append(balancers, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list balancers: %w", err)
	}

	for i := range balancers {
		backends, err := r.backendsOf(ctx, balancers[i].ID)
		if err != nil {
			return nil, err
		}
		balancers[i].Backends = backends

		routes, err := r.routesOf(ctx, balancers[i].ID)
		if err != nil {
			return nil, err
		}
		balancers[i].Routes = routes
	}
	return balancers, nil
}

func (r *repository) backendsOf(ctx context.Context, balancerID string) ([]Backend, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT b.instance_id, b.added_at, h.healthy, h.reason, h.checked_at
			FROM balancer_backends b
			LEFT JOIN balancer_health h
				ON h.balancer_id = b.balancer_id AND h.instance_id = b.instance_id
			WHERE b.balancer_id = ? ORDER BY b.instance_id`, balancerID)
	if err != nil {
		return nil, fmt.Errorf("list backends: %w", err)
	}
	defer rows.Close()

	backends := make([]Backend, 0, 4)
	for rows.Next() {
		var (
			backend Backend
			added   string
			passed  sql.NullBool
			reason  sql.NullString
			checked sql.NullString
		)
		if err := rows.Scan(&backend.InstanceID, &added, &passed, &reason, &checked); err != nil {
			return nil, fmt.Errorf("scan a backend: %w", err)
		}

		at, err := time.Parse(time.RFC3339Nano, added)
		if err != nil {
			return nil, fmt.Errorf("parse added_at: %w", err)
		}
		backend.AddedAt = at

		if checked.Valid {
			when, err := time.Parse(time.RFC3339Nano, checked.String)
			if err != nil {
				return nil, fmt.Errorf("parse checked_at: %w", err)
			}
			backend.CheckedAt = when
			backend.Probe = ProbeFailing
			if passed.Bool {
				backend.Probe = ProbePassing
			}
			backend.Reason = reason.String
		}
		backends = append(backends, backend)
	}
	return backends, rows.Err()
}

func (r *repository) replaceRoutes(ctx context.Context, balancerID string,
	routes []Route) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM balancer_routes WHERE balancer_id = ?`, balancerID); err != nil {
		return fmt.Errorf("clear the routes: %w", err)
	}

	for _, one := range routes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO balancer_routes (balancer_id, host, path, service_id)
				VALUES (?, ?, ?, ?)`,
			balancerID, one.Host, one.Path, one.ServiceID); err != nil {
			return fmt.Errorf("insert a route: %w", err)
		}
	}
	return tx.Commit()
}

func (r *repository) routesOf(ctx context.Context, balancerID string) ([]Route, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT host, path, service_id FROM balancer_routes
			WHERE balancer_id = ? ORDER BY host, path`, balancerID)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	defer rows.Close()

	routes := make([]Route, 0, 4)
	for rows.Next() {
		var one Route
		if err := rows.Scan(&one.Host, &one.Path, &one.ServiceID); err != nil {
			return nil, fmt.Errorf("scan a route: %w", err)
		}
		routes = append(routes, one)
	}
	return routes, rows.Err()
}

func (r *repository) healthOf(ctx context.Context, balancerID string) (map[string]Backend, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT instance_id, healthy, reason, checked_at FROM balancer_health
			WHERE balancer_id = ?`, balancerID)
	if err != nil {
		return nil, fmt.Errorf("list health: %w", err)
	}
	defer rows.Close()

	health := map[string]Backend{}
	for rows.Next() {
		var (
			backend Backend
			passed  bool
			checked string
		)
		if err := rows.Scan(&backend.InstanceID, &passed, &backend.Reason, &checked); err != nil {
			return nil, fmt.Errorf("scan health: %w", err)
		}

		when, err := time.Parse(time.RFC3339Nano, checked)
		if err != nil {
			return nil, fmt.Errorf("parse checked_at: %w", err)
		}
		backend.CheckedAt = when
		backend.Probe = ProbeFailing
		if passed {
			backend.Probe = ProbePassing
		}
		health[backend.InstanceID] = backend
	}
	return health, rows.Err()
}

func (r *repository) saveHealth(ctx context.Context, reports []Report, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stamp := at.Format(time.RFC3339Nano)
	for _, report := range reports {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO balancer_health (balancer_id, instance_id, healthy, reason, checked_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (balancer_id, instance_id) DO UPDATE SET
					healthy = excluded.healthy,
					reason = excluded.reason,
					checked_at = excluded.checked_at`,
			report.BalancerID, report.InstanceID, report.Healthy, report.Reason, stamp)
		if err != nil {
			return fmt.Errorf("save a health report: %w", err)
		}
	}
	return tx.Commit()
}

func (r *repository) forgetHealth(ctx context.Context, balancerID, instanceID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM balancer_health WHERE balancer_id = ? AND instance_id = ?`,
		balancerID, instanceID)
	if err != nil {
		return fmt.Errorf("forget a health report: %w", err)
	}
	return nil
}

func (r *repository) delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM balancer_backends WHERE balancer_id = ?`, id); err != nil {
		return fmt.Errorf("delete the backends: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM balancer_health WHERE balancer_id = ?`, id); err != nil {
		return fmt.Errorf("delete the health reports: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM balancer_routes WHERE balancer_id = ?`, id); err != nil {
		return fmt.Errorf("delete the routes: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM balancers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete balancer: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete balancer: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return tx.Commit()
}

func (r *repository) releaseInstance(ctx context.Context, instanceID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM balancer_backends WHERE instance_id = ?`, instanceID); err != nil {
		return fmt.Errorf("release an instance from its balancers: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM balancer_health WHERE instance_id = ?`, instanceID); err != nil {
		return fmt.Errorf("forget the health reports of an instance: %w", err)
	}
	return tx.Commit()
}

type scanner interface {
	Scan(dest ...any) error
}

func (r *repository) setTLS(ctx context.Context, id string, t TLS) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE balancers SET tls_material = ?, tls_key_id = ?, tls_subject = ?,
			tls_expires_at = ? WHERE id = ?`,
		t.Material, t.KeyID, t.Subject, t.ExpiresAt, id)
	if err != nil {
		return fmt.Errorf("set the balancer certificate: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set the balancer certificate: %w", err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func scan(row scanner) (Balancer, error) {
	var (
		b       Balancer
		created string
	)
	if err := row.Scan(&b.ID, &b.ProjectID, &b.Name, &b.Protocol, &b.ListenPort, &b.TargetPort,
		&b.Algorithm, &b.ServiceID, &b.Check, &b.CheckPath, &b.Rise, &b.Fall, &b.Family,
		&b.TLS.Material, &b.TLS.KeyID, &b.TLS.Subject, &b.TLS.ExpiresAt,
		&created); err != nil {
		return Balancer{}, fmt.Errorf("scan balancer: %w", err)
	}

	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Balancer{}, fmt.Errorf("parse created_at: %w", err)
	}
	b.CreatedAt = at
	return b, nil
}

func classify(err error, action string) error {
	if !isUniqueViolation(err) {
		return fmt.Errorf("%s: %w", action, err)
	}
	if strings.Contains(err.Error(), "listen_port") {
		return errPortUsed
	}
	return errNameUsed
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
