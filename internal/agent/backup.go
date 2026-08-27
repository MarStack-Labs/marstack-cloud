package agent

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (a *Agent) archivers() []workload.VolumeArchiver {
	found := make([]workload.VolumeArchiver, 0, len(a.runtimes))
	for _, runtime := range a.runtimes {
		if archiver, able := runtime.(workload.VolumeArchiver); able {
			found = append(found, archiver)
		}
	}
	return found
}

func (a *Agent) restoreVolumes(
	ctx context.Context, nodeID string, volumes []volumeView, assigned []instanceView,
) {
	archivers := a.archivers()
	if len(archivers) == 0 {
		return
	}

	for _, v := range volumes {
		if v.BackupID == "" {
			continue
		}
		if a.volumeIsBusy(ctx, v, assigned) {
			continue
		}
		for _, archiver := range archivers {
			if archiver.HasVolume(v.ID) {
				continue
			}
			a.restoreVolume(ctx, nodeID, v, archiver)
		}
	}
}

func (a *Agent) restoreVolume(
	ctx context.Context, nodeID string, v volumeView, archiver workload.VolumeArchiver,
) {
	content, err := a.client.fetchBackup(ctx, nodeID, v.BackupID)
	if err != nil {
		a.log.Warn("could not fetch the backup a volume restores from",
			"volume", v.ID, "backup", v.BackupID, "error", err)
		return
	}
	defer content.Close()

	if err := archiver.ImportVolume(v.ID, content); err != nil {
		a.log.Warn("could not write the restored volume",
			"volume", v.ID, "backup", v.BackupID, "error", err)
		return
	}

	a.log.Info("volume restored from a backup",
		"volume", v.ID, "name", v.Name, "backup", v.BackupID)
}

func (a *Agent) runBackups(
	ctx context.Context, nodeID string, volumes []volumeView, assigned []instanceView,
) {
	archivers := a.archivers()
	if len(archivers) == 0 {
		return
	}

	pending, err := a.client.pendingBackups(ctx, nodeID)
	if err != nil {
		a.log.Warn("could not read the pending backups", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	byVolume := make(map[string]volumeView, len(volumes))
	for _, v := range volumes {
		byVolume[v.ID] = v
	}

	for _, b := range pending {
		v, known := byVolume[b.VolumeID]
		if !known {
			continue
		}
		if a.volumeIsBusy(ctx, v, assigned) {
			continue
		}

		keyFile := ""
		if v.Encrypted {
			path, err := a.volumeKeyFile(ctx, v.ID)
			if err != nil {
				a.reportBackupFailure(ctx, nodeID, b,
					"the key of this encrypted volume could not be placed: "+err.Error())
				continue
			}
			keyFile = path
		}
		a.runBackup(ctx, nodeID, b, keyFile, archivers)
	}
}

func (a *Agent) runBackup(
	ctx context.Context, nodeID string, b backupView, keyFile string,
	archivers []workload.VolumeArchiver,
) {
	for _, archiver := range archivers {
		if !archiver.HasVolume(b.VolumeID) {
			continue
		}

		content, err := archiver.ExportVolume(b.VolumeID, keyFile)
		if err != nil {
			a.reportBackupFailure(ctx, nodeID, b, err.Error())
			return
		}

		err = a.client.uploadBackup(ctx, nodeID, b.ID, content)
		content.Close()
		if err != nil {
			a.log.Warn("could not upload a backup",
				"backup", b.ID, "volume", b.VolumeID, "error", err)
			return
		}

		a.log.Info("backup copied off the node",
			"backup", b.ID, "name", b.Name, "volume", b.VolumeID)
		return
	}

	a.reportBackupFailure(ctx, nodeID, b, "this node holds no file for that volume")
}

func (a *Agent) reportBackupFailure(ctx context.Context, nodeID string, b backupView, message string) {
	a.log.Warn("could not copy a volume for a backup",
		"backup", b.ID, "volume", b.VolumeID, "reason", message)

	if err := a.client.failBackup(ctx, nodeID, b.ID, message); err != nil {
		a.log.Warn("could not report the failed backup", "backup", b.ID, "error", err)
	}
}
