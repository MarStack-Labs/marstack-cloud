package container

const (
	DefaultRoot   = "/var/lib/marstack"
	cgroupRoot    = "/sys/fs/cgroup"
	cgroupSlice   = "marstack"
	initEnvConfig = "MARSTACK_INIT_CONFIG"
	shmSize       = "64m"
)
