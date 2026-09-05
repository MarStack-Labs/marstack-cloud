package volume

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/page"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Instances interface {
	Placement(ctx context.Context, ref, projectID string) (Placement, error)
}

type Quota interface {
	AdmitVolume(ctx context.Context, projectID string, sizeGiB int) error
	AdmitVolumeGrowth(ctx context.Context, projectID string, extraGiB int) error
}

type Backups interface {
	Restorable(ctx context.Context, id, projectID string) (Restorable, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	keys      *sealed.Keyring
	quota     Quota
	backups   Backups
	instances Instances
	now       clock
}

func newService(repo *repository, instances Instances, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, instances: instances, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Volume, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Volume{}, err
	}
	if params.SizeGiB < MinSizeGiB || params.SizeGiB > MaxSizeGiB {
		return Volume{}, fault.Invalid("invalid_size", fmt.Sprintf(
			"size_gib must be between %d and %d", MinSizeGiB, MaxSizeGiB,
		))
	}

	if s.quota != nil {
		if err := s.quota.AdmitVolume(ctx, params.ProjectID, params.SizeGiB); err != nil {
			return Volume{}, err
		}
	}

	guard, err := s.guardFor(ctx, params)
	if err != nil {
		return Volume{}, err
	}

	now := s.now()
	v := Volume{
		ID:        ids.New("vol"),
		ProjectID: params.ProjectID,
		Name:      params.Name,
		BackupID:  params.BackupID,
		Encrypted: guard.encrypts(),
		KeySealed: guard.sealedKey,
		KeyID:     guard.keyID,
		SizeGiB:   params.SizeGiB,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.insert(ctx, v); err != nil {
		return Volume{}, translate(err)
	}
	return v, nil
}

func (s *service) clone(ctx context.Context, snapshotID, projectID, name string) (Volume, error) {
	if err := validate.Name("name", name); err != nil {
		return Volume{}, err
	}

	snap, err := s.ownedSnapshot(ctx, snapshotID, projectID)
	if err != nil {
		return Volume{}, err
	}
	if snap.State != SnapshotReady {
		return Volume{}, fault.Conflict("snapshot_not_ready",
			"the snapshot is "+snap.State+" and there is nothing to copy yet")
	}

	source, err := s.repo.byID(ctx, snap.VolumeID)
	if err != nil {
		return Volume{}, translate(err)
	}
	if source.NodeID == "" {
		return Volume{}, fault.Conflict("volume_not_placed",
			"a snapshot lives inside the disk file on the node that holds the volume, and "+
				"this volume is not on a node, so there is nothing to copy from")
	}
	if source.Encrypted {
		return Volume{}, fault.Conflict("volume_encrypted",
			"copying an encrypted volume would either need its key on a second disk or "+
				"write the contents out in the clear, and neither is something to do "+
				"quietly: restore it onto a volume of its own instead")
	}

	if s.quota != nil {
		if err := s.quota.AdmitVolume(ctx, projectID, source.SizeGiB); err != nil {
			return Volume{}, err
		}
	}

	now := s.now()
	v := Volume{
		ID:        ids.New("vol"),
		ProjectID: projectID,
		Name:      name,
		SizeGiB:   source.SizeGiB,
		NodeID:    source.NodeID,
		CloneFrom: source.ID,
		CloneSnap: snap.Name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.insert(ctx, v); err != nil {
		return Volume{}, translate(err)
	}

	return v, nil
}

func (s *service) resolveIn(ctx context.Context, nameOrID, projectID string) (Volume, error) {
	v, err := s.repo.byName(ctx, projectID, nameOrID)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, errNotFound) {
		return Volume{}, translate(err)
	}

	v, err = s.repo.byID(ctx, nameOrID)
	if err != nil {
		return Volume{}, translate(err)
	}
	if v.ProjectID != projectID {
		return Volume{}, fault.NotFound("volume_not_found", "no volume with that name or id exists")
	}
	return v, nil
}

func (s *service) footprintIn(ctx context.Context, projectID string) (Footprint, error) {
	footprint, err := s.repo.footprintIn(ctx, projectID)
	if err != nil {
		return Footprint{}, translate(err)
	}
	return footprint, nil
}

func (s *service) pageIn(
	ctx context.Context, projectID string, window page.Window,
) ([]Volume, error) {
	rows, err := s.repo.pageIn(ctx, projectID, window)
	if err != nil {
		return nil, translate(err)
	}
	return rows, nil
}

func (s *service) onNode(ctx context.Context, nodeID string) ([]Volume, error) {
	volumes, err := s.repo.onNode(ctx, nodeID)
	if err != nil {
		return nil, translate(err)
	}
	return volumes, nil
}

func (s *service) attach(
	ctx context.Context, nameOrID, projectID, instanceRef string,
) (Volume, error) {
	v, err := s.resolveIn(ctx, nameOrID, projectID)
	if err != nil {
		return Volume{}, err
	}
	if s.instances == nil {
		return Volume{}, fault.Unavailable("instances_unavailable",
			"the platform cannot look up where an instance runs")
	}

	placed, err := s.instances.Placement(ctx, instanceRef, projectID)
	if err != nil {
		return Volume{}, err
	}
	instanceID := placed.InstanceID

	if v.InstanceID == instanceID {
		if v.Detaching {
			if err := s.repo.clearDetaching(ctx, v.ID, s.now()); err != nil {
				return Volume{}, translate(err)
			}
			v.Detaching = false
		}
		return v, nil
	}
	if v.InstanceID != "" {
		return Volume{}, fault.Conflict("volume_attached",
			"the volume is attached to "+v.InstanceID+", and a disk cannot have two writers")
	}
	if placed.ProjectID != projectID {
		return Volume{}, fault.NotFound("instance_not_found",
			"no instance with that name or id exists")
	}
	if placed.NodeID == "" {
		return Volume{}, fault.Conflict("instance_unplaced",
			"the instance has no node yet, and a volume lives on the node it is attached to")
	}
	if placed.Isolation != "vm" {
		return Volume{}, fault.Invalid("unsupported_isolation",
			"only isolation vm takes a volume today, because the others have no second disk")
	}
	if v.NodeID != "" && v.NodeID != placed.NodeID {
		return Volume{}, fault.Conflict("volume_elsewhere",
			"the volume holds data on node "+v.NodeID+" and cannot follow an instance to "+
				placed.NodeID)
	}

	if err := s.repo.attach(ctx, v.ID, instanceID, placed.NodeID, s.now()); err != nil {
		return Volume{}, translate(err)
	}

	v.InstanceID = instanceID
	v.NodeID = placed.NodeID
	return v, nil
}

func (s *service) detach(ctx context.Context, nameOrID, projectID string) (Volume, error) {
	v, err := s.resolveIn(ctx, nameOrID, projectID)
	if err != nil {
		return Volume{}, err
	}
	if v.InstanceID == "" {
		return v, nil
	}

	if s.holdsIt(ctx, v) {
		if err := s.repo.setDetaching(ctx, v.ID, s.now()); err != nil {
			return Volume{}, translate(err)
		}
		v.Detaching = true
		return v, nil
	}

	if err := s.repo.detach(ctx, v.ID, s.now()); err != nil {
		return Volume{}, translate(err)
	}

	v.InstanceID = ""
	v.Detaching = false
	return v, nil
}

func (s *service) holdsIt(ctx context.Context, v Volume) bool {
	if s.instances == nil {
		return false
	}

	placed, err := s.instances.Placement(ctx, v.InstanceID, v.ProjectID)
	if err != nil {
		return false
	}
	return placed.Observed == "running" && placed.NodeID != ""
}

func (s *service) releaseInstance(ctx context.Context, instanceID string) error {
	if err := s.repo.detachInstance(ctx, instanceID, s.now()); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) quiet(ctx context.Context, v Volume, action string) error {
	if v.NodeID == "" {
		return fault.Conflict("volume_empty",
			"the volume has never been attached, so there is nothing on disk to "+action)
	}
	return s.idle(ctx, v, action)
}

func (s *service) idle(ctx context.Context, v Volume, action string) error {
	if v.InstanceID == "" || s.instances == nil {
		return nil
	}

	placed, err := s.instances.Placement(ctx, v.InstanceID, v.ProjectID)
	if err != nil {
		return err
	}
	if placed.Running {
		return fault.Conflict("instance_running",
			"stop "+v.InstanceID+" first: writing to a disk while qemu has it open would "+
				action+" an image that is changing underneath")
	}
	return nil
}

func (s *service) snapshot(
	ctx context.Context, nameOrID, projectID, name string,
) (Snapshot, error) {
	return s.snapshotWithSchedule(ctx, nameOrID, projectID, name, "")
}

func (s *service) snapshotWithSchedule(
	ctx context.Context, nameOrID, projectID, name, scheduleID string,
) (Snapshot, error) {
	v, err := s.resolveIn(ctx, nameOrID, projectID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validate.Name("name", name); err != nil {
		return Snapshot{}, err
	}
	if err := s.quiet(ctx, v, "snapshot"); err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{
		ID:         ids.New("snap"),
		VolumeID:   v.ID,
		Name:       name,
		State:      SnapshotPending,
		ScheduleID: scheduleID,
		CreatedAt:  s.now(),
	}

	if err := s.repo.insertSnapshot(ctx, snap); err != nil {
		if errors.Is(err, errNameTaken) {
			return Snapshot{}, fault.Conflict("snapshot_name_taken",
				"the volume already has a snapshot with that name")
		}
		return Snapshot{}, translate(err)
	}
	return snap, nil
}

func (s *service) snapshots(ctx context.Context, volumeID string) ([]Snapshot, error) {
	snapshots, err := s.repo.snapshots(ctx, volumeID)
	if err != nil {
		return nil, translate(err)
	}
	return snapshots, nil
}

func (s *service) snapshotsPageIn(
	ctx context.Context, projectID string, window page.Window,
) ([]Snapshot, error) {
	snaps, err := s.repo.snapshotsPageIn(ctx, projectID, window)
	if err != nil {
		return nil, translate(err)
	}
	return snaps, nil
}

func (s *service) ownedSnapshot(ctx context.Context, id, projectID string) (Snapshot, error) {
	snap, err := s.repo.snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, translate(err)
	}

	v, err := s.repo.byID(ctx, snap.VolumeID)
	if err != nil {
		return Snapshot{}, translate(err)
	}
	if v.ProjectID != projectID {
		return Snapshot{}, fault.NotFound("snapshot_not_found", "no snapshot with that id exists")
	}
	return snap, nil
}

func (s *service) removeSnapshot(ctx context.Context, id, projectID string) error {
	if _, err := s.ownedSnapshot(ctx, id, projectID); err != nil {
		return err
	}
	if err := s.repo.deleteSnapshot(ctx, id); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) restore(ctx context.Context, snapshotID, projectID string) (Volume, error) {
	snap, err := s.ownedSnapshot(ctx, snapshotID, projectID)
	if err != nil {
		return Volume{}, err
	}
	if snap.State != SnapshotReady {
		return Volume{}, fault.Conflict("snapshot_not_ready",
			"the snapshot is "+snap.State+" and cannot be restored yet")
	}

	v, err := s.repo.byID(ctx, snap.VolumeID)
	if err != nil {
		return Volume{}, translate(err)
	}
	if err := s.quiet(ctx, v, "restore"); err != nil {
		return Volume{}, err
	}

	if err := s.repo.setRestore(ctx, v.ID, snap.Name, s.now()); err != nil {
		return Volume{}, translate(err)
	}

	v.RestoreFrom = snap.Name
	return v, nil
}

func (s *service) report(ctx context.Context, nodeID string, reports []NodeReport) error {
	for _, reported := range reports {
		v, err := s.repo.byID(ctx, reported.VolumeID)
		if err != nil {
			continue
		}
		if v.NodeID != nodeID {
			continue
		}

		if reported.Detached && v.Detaching {
			if err := s.repo.detach(ctx, v.ID, s.now()); err != nil {
				return translate(err)
			}
		}

		present := map[string]int64{}
		for _, file := range reported.Snapshots {
			present[file.Name] = file.SizeBytes
		}

		known, err := s.repo.snapshots(ctx, v.ID)
		if err != nil {
			return translate(err)
		}

		for _, snap := range known {
			size, exists := present[snap.Name]
			switch {
			case exists && snap.State != SnapshotReady:
				err = s.repo.markSnapshot(ctx, snap.ID, SnapshotReady, "", size)
			case exists:
				err = s.repo.markSnapshot(ctx, snap.ID, SnapshotReady, "", size)
			case reported.Error != "":
				err = s.repo.markSnapshot(ctx, snap.ID, SnapshotFailed, reported.Error, 0)
			default:
				continue
			}
			if err != nil {
				return translate(err)
			}
		}

		if reported.Restored != "" && reported.Restored == v.RestoreFrom {
			if err := s.repo.setRestore(ctx, v.ID, "", s.now()); err != nil {
				return translate(err)
			}
		}
	}
	return nil
}

func (s *service) remove(ctx context.Context, nameOrID, projectID string) error {
	v, err := s.resolveIn(ctx, nameOrID, projectID)
	if err != nil {
		return err
	}
	if v.InstanceID != "" {
		return fault.Conflict("volume_attached",
			"detach the volume from "+v.InstanceID+" before deleting it")
	}

	if err := s.repo.delete(ctx, v.ID); err != nil {
		return translate(err)
	}
	return nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("volume_not_found", "no volume with that name or id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("volume_name_taken", "a volume with that name already exists")
	case errors.Is(err, errTaken):
		return fault.Conflict("volume_attached", "the volume was attached by someone else")
	default:
		return err
	}
}

type volumeKey struct {
	sealedKey string
	keyID     string
	adopted   bool
}

func (k volumeKey) encrypts() bool {
	return k.sealedKey != ""
}

type Restorable struct {
	SizeBytes int64
	KeySealed string
	KeyID     string
}

func (s *service) guardFor(ctx context.Context, params CreateParams) (volumeKey, error) {
	if params.BackupID == "" {
		return s.mintKey(params.Encrypted)
	}

	if s.backups == nil {
		return volumeKey{}, fault.Unavailable("backups_unavailable",
			"the platform cannot look up backups")
	}

	envelope, err := s.backups.Restorable(ctx, params.BackupID, params.ProjectID)
	if err != nil {
		return volumeKey{}, err
	}

	if envelope.KeySealed == "" {
		if params.Encrypted {
			return volumeKey{}, fault.Conflict("backup_not_encrypted",
				"that backup holds a plaintext volume, and the node writes a restore byte for "+
					"byte, so the result would be a plaintext disk claiming to be encrypted")
		}
		return volumeKey{}, nil
	}

	if _, held := s.keys.Find(envelope.KeyID); !held {
		return volumeKey{}, fault.Unavailable("key_missing",
			"that backup's volume key was sealed with key "+envelope.KeyID+
				", which this control plane does not hold")
	}
	return volumeKey{sealedKey: envelope.KeySealed, keyID: envelope.KeyID, adopted: true}, nil
}

func (s *service) mintKey(wanted bool) (volumeKey, error) {
	if !wanted {
		return volumeKey{}, nil
	}

	active, ok := s.keys.Active()
	if !ok {
		return volumeKey{}, fault.Unavailable("no_encryption_key",
			"this control plane holds no key to protect a volume key with, so an encrypted "+
				"volume would only look encrypted; start it with --backup-key-file")
	}

	raw := make([]byte, sealed.KeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return volumeKey{}, fault.Internal(err)
	}

	wrapped, err := sealed.SealBytes(raw, active)
	if err != nil {
		return volumeKey{}, fault.Internal(err)
	}
	return volumeKey{sealedKey: wrapped, keyID: active.ID()}, nil
}

func (s *service) keyForNode(ctx context.Context, volumeID, nodeID string) (string, error) {
	v, err := s.repo.byID(ctx, volumeID)
	if err != nil {
		return "", translate(err)
	}
	if v.NodeID != nodeID {
		return "", fault.NotFound("volume_not_found", "no volume with that id is held here")
	}
	if !v.Encrypted {
		return "", fault.Conflict("volume_not_encrypted", "that volume carries no key")
	}

	key, known := s.keys.Find(v.KeyID)
	if !known {
		return "", fault.Unavailable("key_missing", "the volume key was sealed with key "+
			v.KeyID+", which this control plane does not hold")
	}

	raw, err := sealed.OpenBytes(v.KeySealed, key)
	if err != nil {
		return "", fault.Internal(err)
	}
	return hex.EncodeToString(raw), nil
}

func (s *service) resize(ctx context.Context, nameOrID, projectID string, sizeGiB int) (Volume, error) {
	v, err := s.resolveIn(ctx, nameOrID, projectID)
	if err != nil {
		return Volume{}, err
	}

	if sizeGiB < MinSizeGiB || sizeGiB > MaxSizeGiB {
		return Volume{}, fault.Invalid("invalid_size", fmt.Sprintf(
			"size_gib must be between %d and %d", MinSizeGiB, MaxSizeGiB))
	}
	if sizeGiB == v.SizeGiB {
		return v, nil
	}
	if sizeGiB < v.SizeGiB {
		return Volume{}, fault.Conflict("volume_shrink",
			fmt.Sprintf("the volume is %d GiB, and shrinking it to %d would mean deciding "+
				"which bytes to lose", v.SizeGiB, sizeGiB))
	}

	if err := s.idle(ctx, v, "resize"); err != nil {
		return Volume{}, err
	}

	if s.quota != nil {
		if err := s.quota.AdmitVolumeGrowth(ctx, v.ProjectID, sizeGiB-v.SizeGiB); err != nil {
			return Volume{}, err
		}
	}

	if err := s.repo.setSize(ctx, v.ID, sizeGiB, s.now()); err != nil {
		return Volume{}, translate(err)
	}

	v.SizeGiB = sizeGiB
	return v, nil
}
