package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type repository struct {
	db *sql.DB
}

func newRepository(st *store.Store) *repository {
	return &repository{db: st.DB()}
}

var errNameTaken = errors.New("instance name already exists")

var errNotFound = errors.New("instance not found")

var errAlreadyPlaced = errors.New("instance is already placed on a node")

const columns = `id, name, isolation, image, command, network_id, vcpu, memory_mib, desired_state, observed_state, observed_message, node_id, created_at, updated_at`

func (r *repository) insert(ctx context.Context, in Instance) error {
	taken, err := r.nameTaken(ctx, in.Name)
	if err != nil {
		return err
	}
	if taken {
		return errNameTaken
	}

	command, err := json.Marshal(in.Command)
	if err != nil {
		return fmt.Errorf("encode command: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO instances (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, string(in.Isolation), in.Image, string(command), in.NetworkID, in.VCPU, in.MemoryMiB,
		string(in.Desired), string(in.Observed), in.ObservedMessage, in.NodeID,
		in.CreatedAt.Format(time.RFC3339Nano), in.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert instance: %w", err)
	}
	return nil
}

func (r *repository) nameTaken(ctx context.Context, name string) (bool, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM instances WHERE name = ?`, name,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check instance name: %w", err)
	}
	return count > 0, nil
}

func (r *repository) get(ctx context.Context, id string) (Instance, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM instances WHERE id = ?`, id)

	in, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, errNotFound
	}
	if err != nil {
		return Instance{}, fmt.Errorf("get instance: %w", err)
	}
	return in, nil
}

func (r *repository) list(ctx context.Context) ([]Instance, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM instances ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()

	instances := make([]Instance, 0)
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan instance: %w", err)
		}
		instances = append(instances, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate instances: %w", err)
	}
	return instances, nil
}

func (r *repository) listPendingPlacement(ctx context.Context) ([]Instance, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM instances
		 WHERE desired_state = ? AND (node_id IS NULL OR node_id = '')
		 ORDER BY created_at, id`,
		string(DesiredRunning),
	)
	if err != nil {
		return nil, fmt.Errorf("list instances pending placement: %w", err)
	}
	defer rows.Close()

	instances := make([]Instance, 0)
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan instance: %w", err)
		}
		instances = append(instances, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate instances: %w", err)
	}
	return instances, nil
}

func (r *repository) listByNode(ctx context.Context, nodeID string) ([]Instance, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM instances WHERE node_id = ? ORDER BY created_at, id`,
		nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list instances by node: %w", err)
	}
	defer rows.Close()

	instances := make([]Instance, 0)
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan instance: %w", err)
		}
		instances = append(instances, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate instances: %w", err)
	}
	return instances, nil
}

func (r *repository) setObserved(
	ctx context.Context, instanceID, nodeID string,
	observed ObservedState, message string, now time.Time,
) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances SET observed_state = ?, observed_message = ?, updated_at = ?
		 WHERE id = ? AND node_id = ?`,
		string(observed), message, now.Format(time.RFC3339Nano), instanceID, nodeID,
	)
	if err != nil {
		return fmt.Errorf("set observed state: %w", err)
	}
	return expectOneRow(res, "set observed state")
}

func (r *repository) assignedCounts(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT node_id, COUNT(*) FROM instances
		 WHERE node_id IS NOT NULL AND node_id != '' GROUP BY node_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("count instances per node: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var (
			nodeID string
			count  int
		)
		if err := rows.Scan(&nodeID, &count); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		counts[nodeID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate counts: %w", err)
	}
	return counts, nil
}

func (r *repository) assign(ctx context.Context, id, nodeID string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances SET node_id = ?, updated_at = ?
		 WHERE id = ? AND (node_id IS NULL OR node_id = '')`,
		nodeID, now.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("assign instance: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("assign instance: %w", err)
	}
	if affected == 0 {
		return errAlreadyPlaced
	}
	return nil
}

func (r *repository) setDesired(ctx context.Context, id string, desired DesiredState, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances SET desired_state = ?, updated_at = ? WHERE id = ?`,
		string(desired), now.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("update desired state: %w", err)
	}
	return expectOneRow(res, "update desired state")
}

func (r *repository) delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	return expectOneRow(res, "delete instance")
}

func expectOneRow(res sql.Result, op string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanInstance(row scanner) (Instance, error) {
	var (
		in         Instance
		isolation  string
		command    string
		desired    string
		observed   string
		nodeID     sql.NullString
		createdRaw string
		updatedRaw string
	)

	if err := row.Scan(
		&in.ID, &in.Name, &isolation, &in.Image, &command, &in.NetworkID, &in.VCPU, &in.MemoryMiB,
		&desired, &observed, &in.ObservedMessage, &nodeID, &createdRaw, &updatedRaw,
	); err != nil {
		return Instance{}, err
	}

	if command != "" {
		if err := json.Unmarshal([]byte(command), &in.Command); err != nil {
			return Instance{}, fmt.Errorf("decode command: %w", err)
		}
	}

	in.Isolation = Isolation(isolation)
	in.Desired = DesiredState(desired)
	in.Observed = ObservedState(observed)
	in.NodeID = nodeID.String

	var err error
	if in.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw); err != nil {
		return Instance{}, fmt.Errorf("parse created_at: %w", err)
	}
	if in.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw); err != nil {
		return Instance{}, fmt.Errorf("parse updated_at: %w", err)
	}

	return in, nil
}
