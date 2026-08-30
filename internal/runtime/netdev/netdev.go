package netdev

const MaxDevices = 4

type Interface struct {
	Bridge     string
	BridgeAddr string
	InstanceID string
	IP         string
	Prefix     int
	Gateway    string
	IP6        string
	Prefix6    int
	Gateway6   string
	MAC        string
	Device     int
}

type Datapath struct{}
