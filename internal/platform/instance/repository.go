package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/page"
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

const columns = `id, project_id, placement_group, placement_strict, ssh_keys, node_selector, env, env_key_id, env_names, files, file_paths, extra_networks, name, isolation, image, iso, kernel, disk_gib, firewall_id, command, network_id, restart_policy, restart_count, vcpu, memory_mib, desired_state, observed_state, observed_message, exit_code, migrating, disk_parked, disk_from, node_id,
	created_at, updated_at`

func (r *repository) insert(ctx context.Context, in Instance) error {
	taken, err := r.nameTaken(ctx, in.ProjectID, in.Name)
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

	keys, err := json.Marshal(in.SSHKeys)
	if err != nil {
		return fmt.Errorf("encode ssh keys: %w", err)
	}

	selector, err := json.Marshal(in.NodeSelector)
	if err != nil {
		return fmt.Errorf("encode the node selector: %w", err)
	}

	envNames, err := json.Marshal(in.EnvNames)
	if err != nil {
		return fmt.Errorf("encode the environment names: %w", err)
	}

	filePaths, err := json.Marshal(in.FilePaths)
	if err != nil {
		return fmt.Errorf("encode the file paths: %w", err)
	}

	extra, err := json.Marshal(in.ExtraNetworks)
	if err != nil {
		return fmt.Errorf("encode the extra networks: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO instances (`+columns+`) VALUES `+
			`(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.ProjectID, in.Group, in.Strict, string(keys), string(selector),
		in.EnvSealed, in.SealKeyID, string(envNames), in.FilesSealed, string(filePaths), string(extra), in.Name, string(in.Isolation), in.Image, in.ISO, in.Kernel, in.DiskGiB,
		in.FirewallID, string(command), in.NetworkID,
		string(in.RestartPolicy), in.RestartCount, in.VCPU, in.MemoryMiB,
		string(in.Desired), string(in.Observed), in.ObservedMessage, in.ExitCode,
		in.Migrating, in.DiskParked, in.DiskFrom, in.NodeID,
		in.CreatedAt.Format(time.RFC3339Nano), in.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errNameTaken
		}
		return fmt.Errorf("insert instance: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (r *repository) nameTaken(ctx context.Context, projectID, name string) (bool, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM instances WHERE project_id = ? AND name = ?`, projectID, name,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check instance name: %w", err)
	}
	return count > 0, nil
}

func (r *repository) pageIn(
	ctx context.Context, projectID string, window page.Window,
) ([]Instance, error) {
	after := window.After
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM instances
			WHERE project_id = ?
				AND (? = '' OR created_at > ? OR (created_at = ? AND id > ?))
			ORDER BY created_at, id LIMIT ?`,
		projectID, after.Order, after.Order, after.Order, after.ID, window.Limit)
	if err != nil {
		return nil, fmt.Errorf("list instances in project: %w", err)
	}
	defer rows.Close()

	instances := make([]Instance, 0, window.Limit)
	for rows.Next() {
		in, scanErr := scanInstance(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		instances = append(instances, in)
	}
	return instances, rows.Err()
}

func (r *repository) byName(ctx context.Context, projectID, name string) (Instance, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM instances WHERE project_id = ? AND name = ?`,
		projectID, name)

	in, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, errNotFound
	}
	if err != nil {
		return Instance{}, fmt.Errorf("get instance by name: %w", err)
	}
	return in, nil
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

func (r *repository) footprintIn(ctx context.Context, projectID string) (Footprint, error) {
	var f Footprint
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(vcpu), 0), COALESCE(SUM(memory_mib), 0)
		 FROM instances WHERE project_id = ?`, projectID,
	).Scan(&f.Instances, &f.VCPU, &f.MemoryMiB)
	if err != nil {
		return Footprint{}, fmt.Errorf("measure the project: %w", err)
	}
	return f, nil
}

func (r *repository) listIn(ctx context.Context, projectID string) ([]Instance, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM instances WHERE project_id = ? ORDER BY created_at, id`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list instances in project: %w", err)
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
	return instances, rows.Err()
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
		 WHERE (desired_state = ? OR migrating = 1)
			AND (node_id IS NULL OR node_id = '')
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
	observed ObservedState, message string, restarts int, exitCode *int, now time.Time,
) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances SET observed_state = ?, observed_message = ?, restart_count = ?,
			exit_code = ?, updated_at = ?
		 WHERE id = ? AND node_id = ?`,
		string(observed), message, restarts, exitCode,
		now.Format(time.RFC3339Nano), instanceID, nodeID,
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

func (r *repository) listRunningOn(ctx context.Context, nodeID string) ([]Instance, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+columns+` FROM instances
		 WHERE (desired_state = ? OR migrating = 1) AND node_id = ?
		 ORDER BY created_at, id`,
		string(DesiredRunning), nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list instances on node: %w", err)
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
	return instances, rows.Err()
}

func (r *repository) setMigrating(ctx context.Context, id string, migrating, parked bool,
	from string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances SET migrating = ?, disk_parked = ?, disk_from = ?, updated_at = ?
			WHERE id = ?`,
		migrating, parked, from, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set the migration flags: %w", err)
	}
	return expectOneRow(res, "set the migration flags")
}

func (r *repository) releasePlacement(ctx context.Context, id, nodeID string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances
		 SET node_id = '', observed_state = ?, observed_message = ?, updated_at = ?
		 WHERE id = ? AND node_id = ?`,
		string(ObservedPending), "the node it ran on stopped reporting, so it is being placed again",
		now.Format(time.RFC3339Nano), id, nodeID,
	)
	if err != nil {
		return fmt.Errorf("release placement: %w", err)
	}
	return expectOneRow(res, "release placement")
}

func (r *repository) setSize(
	ctx context.Context, id string, vcpu, memoryMiB int, now time.Time,
) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE instances SET vcpu = ?, memory_mib = ?, updated_at = ? WHERE id = ?`,
		vcpu, memoryMiB, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("resize instance: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("resize instance: %w", err)
	}
	if affected == 0 {
		return errNotFound
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
		keys       string
		selector   string
		envNames   string
		filePaths  string
		extra      string
		policy     string
		desired    string
		observed   string
		nodeID     sql.NullString
		createdRaw string
		updatedRaw string
	)

	if err := row.Scan(
		&in.ID, &in.ProjectID, &in.Group, &in.Strict, &keys, &selector,
		&in.EnvSealed, &in.SealKeyID, &envNames, &in.FilesSealed, &filePaths, &extra,
		&in.Name, &isolation, &in.Image,
		&in.ISO, &in.Kernel, &in.DiskGiB,
		&in.FirewallID, &command, &in.NetworkID,
		&policy, &in.RestartCount, &in.VCPU, &in.MemoryMiB,
		&desired, &observed, &in.ObservedMessage, &in.ExitCode,
		&in.Migrating, &in.DiskParked, &in.DiskFrom, &nodeID, &createdRaw, &updatedRaw,
	); err != nil {
		return Instance{}, err
	}

	if keys != "" {
		if err := json.Unmarshal([]byte(keys), &in.SSHKeys); err != nil {
			return Instance{}, fmt.Errorf("decode ssh keys: %w", err)
		}
	}

	if extra != "" {
		if err := json.Unmarshal([]byte(extra), &in.ExtraNetworks); err != nil {
			return Instance{}, fmt.Errorf("decode the extra networks: %w", err)
		}
	}

	if filePaths != "" {
		if err := json.Unmarshal([]byte(filePaths), &in.FilePaths); err != nil {
			return Instance{}, fmt.Errorf("decode the file paths: %w", err)
		}
	}

	if envNames != "" {
		if err := json.Unmarshal([]byte(envNames), &in.EnvNames); err != nil {
			return Instance{}, fmt.Errorf("decode the environment names: %w", err)
		}
	}

	if selector != "" {
		if err := json.Unmarshal([]byte(selector), &in.NodeSelector); err != nil {
			return Instance{}, fmt.Errorf("decode the node selector: %w", err)
		}
	}

	if command != "" {
		if err := json.Unmarshal([]byte(command), &in.Command); err != nil {
			return Instance{}, fmt.Errorf("decode command: %w", err)
		}
	}

	in.Isolation = Isolation(isolation)
	in.RestartPolicy = RestartPolicy(policy)
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

func (r *repository) groupCounts(ctx context.Context, group string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT node_id, COUNT(*) FROM instances
		 WHERE placement_group = ? AND node_id IS NOT NULL AND node_id != ''
		 GROUP BY node_id`, group)
	if err != nil {
		return nil, fmt.Errorf("count a placement group: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var (
			nodeID string
			count  int
		)
		if err := rows.Scan(&nodeID, &count); err != nil {
			return nil, fmt.Errorf("scan a placement group: %w", err)
		}
		counts[nodeID] = count
	}
	return counts, rows.Err()
}

func (r *repository) setObservedMessage(ctx context.Context, id, message string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE instances SET observed_message = ?, updated_at = ? WHERE id = ?`,
		message, at.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("record why a placement was held: %w", err)
	}
	return nil
}
