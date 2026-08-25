package netdev

type Interface struct {
	Bridge     string
	BridgeAddr string
	InstanceID string
	IP         string
	Prefix     int
	Gateway    string
	MAC        string
}

type Datapath struct{}
