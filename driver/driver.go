package driver

import (
	"fmt"
	"github.com/docker/go-plugins-helpers/network"
	"github.com/vishvananda/netlink"
	"github.com/yunify/docker-plugin-hostnic/log"
	"net"
	"sync"
)

const (
	networkType         = "hostnic"
	containerVethPrefix = "eth"
)

type NicTable map[string]*HostNic

type HostNic struct {
	Name         string // e.g., "en0", "lo0", "eth0.100"
	HardwareAddr string
	Address      string
	endpoint     *Endpoint
}

type Endpoint struct {
	id      string
	hostNic *HostNic
	srcName string
	//portMapping []types.PortBinding // Operation port bindings
	dbIndex    uint64
	dbExists   bool
	sandboxKey string
}

func New() *HostNicDriver {
	d := &HostNicDriver{
		endpoints: make(map[string]*Endpoint),
		lock:      sync.RWMutex{},
		nics:      make(NicTable),
	}
	return d
}

//HostNicDriver implements github.com/docker/go-plugins-helpers/network.Driver
type HostNicDriver struct {
	network   string
	nics      NicTable
	endpoints map[string]*Endpoint
	lock      sync.RWMutex
	ipv4Data  *network.IPAMData
}

func (d *HostNicDriver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	return &network.CapabilitiesResponse{Scope: network.LocalScope}, nil
}

func (d *HostNicDriver) CreateNetwork(r *network.CreateNetworkRequest) error {
	log.Debug("CreateNetwork Called: [ %+v ]", r)
	log.Debug("CreateNetwork IPv4Data len : [ %v ]", len(r.IPv4Data))
	d.lock.Lock()
	defer d.lock.Unlock()

	if d.network != "" {
		fmt.Errorf("only one instance of %s network is allowed,  network [%s] exist.", networkType, d.network)
	}
	d.network = r.NetworkID
	if r.IPv4Data != nil && len(r.IPv4Data) > 0 {
		d.ipv4Data = r.IPv4Data[0]
		log.Debug("CreateNetwork IPv4Data : [ %+v ]", d.ipv4Data)
	}
	return nil
}
func (d *HostNicDriver) AllocateNetwork(r *network.AllocateNetworkRequest) (*network.AllocateNetworkResponse, error) {
	log.Debug("AllocateNetwork Called: [ %+v ]", r)
	return nil, nil
}
func (d *HostNicDriver) DeleteNetwork(r *network.DeleteNetworkRequest) error {
	log.Debug("DeleteNetwork Called: [ %+v ]", r)
	d.lock.Lock()
	defer d.lock.Unlock()
	d.network = ""
	d.ipv4Data = nil
	d.endpoints = make(map[string]*Endpoint)
	d.nics = make(NicTable)

	return nil
}
func (d *HostNicDriver) FreeNetwork(r *network.FreeNetworkRequest) error {
	log.Debug("FreeNetwork Called: [ %+v ]", r)
	return nil
}
func (d *HostNicDriver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	log.Debug("CreateEndpoint Called: [ %+v ]", r)
	log.Debug("r.Interface: [ %+v ]", r.Interface)

	var hostNic *HostNic

	if r.Interface.MacAddress == "" {
		return nil, fmt.Errorf("Please set --mac-address argument. Request interface [%+v] ", r.Interface)
	}

	hostNic = d.FindNicByHardwareAddr(r.Interface.MacAddress)

	if hostNic == nil {
		return nil, fmt.Errorf("Can not find host nic by mac address [%+v] ", r.Interface.MacAddress)
	}

	if hostNic.endpoint != nil {
		return nil, fmt.Errorf("Host nic [%s] has bind to endpoint [ %+v ] ", hostNic.Name, hostNic.endpoint)
	}

	hostNic.Address = r.Interface.Address

	//TODO check host ip and driver network

	hostIfName := hostNic.Name

	endpoint := &Endpoint{}

	// Store the sandbox side pipe interface parameters
	endpoint.srcName = hostIfName
	endpoint.hostNic = hostNic
	endpoint.id = r.EndpointID

	d.endpoints[endpoint.id] = endpoint
	hostNic.endpoint = endpoint

	endpointInterface := &network.EndpointInterface{}
	if r.Interface.Address == "" {
		endpointInterface.Address = hostNic.Address
	}
	if r.Interface.MacAddress == "" {
		endpointInterface.MacAddress = hostNic.HardwareAddr
	}
	resp := &network.CreateEndpointResponse{Interface: endpointInterface}
	log.Debug("CreateEndpoint resp interface: [ %+v ] ", resp.Interface)
	return resp, nil
}

func (d *HostNicDriver) EndpointInfo(r *network.InfoRequest) (*network.InfoResponse, error) {
	log.Debug("EndpointInfo Called: [ %+v ]", r)
	d.lock.RLock()
	defer d.lock.RUnlock()
	endpoint := d.endpoints[r.EndpointID]
	if endpoint == nil {
		return nil, fmt.Errorf("Cannot find endpoint by id: %s", r.EndpointID)
	}
	value := make(map[string]string)
	value["id"] = endpoint.id
	value["srcName"] = endpoint.srcName
	value["hostNic.Name"] = endpoint.hostNic.Name
	value["hostNic.Addr"] = endpoint.hostNic.Address
	value["hostNic.HardwareAddr"] = endpoint.hostNic.HardwareAddr
	resp := &network.InfoResponse{
		Value: value,
	}
	log.Debug("EndpointInfo resp.Value : [ %+v ]", resp.Value)
	return resp, nil
}
func (d *HostNicDriver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	log.Debug("Join Called: [ %+v ]", r)
	endpoint := d.endpoints[r.EndpointID]

	if endpoint == nil {
		return nil, fmt.Errorf("Cannot find endpoint by id: %s", r.EndpointID)
	}

	if endpoint.sandboxKey != "" {
		return nil, fmt.Errorf("Endpoint [%s] has bean bind to sandbox [%s]", r.EndpointID, endpoint.sandboxKey)
	}

	endpoint.sandboxKey = r.SandboxKey
	resp := network.JoinResponse{
		InterfaceName: network.InterfaceName{SrcName: endpoint.srcName, DstPrefix: containerVethPrefix},
	}

	log.Debug("Join resp : [ %+v ]", resp)
	return &resp, nil
}
func (d *HostNicDriver) Leave(r *network.LeaveRequest) error {
	log.Debug("Leave Called: [ %+v ]", r)
	d.lock.Lock()
	defer d.lock.Unlock()

	endpoint := d.endpoints[r.EndpointID]

	if endpoint == nil {
		return fmt.Errorf("Cannot find endpoint by id: %s", r.EndpointID)
	}
	endpoint.sandboxKey = ""

	return nil
}

func (d *HostNicDriver) DeleteEndpoint(r *network.DeleteEndpointRequest) error {
	log.Debug("DeleteEndpoint Called: [ %+v ]", r)
	d.lock.Lock()
	defer d.lock.Unlock()

	endpoint := d.endpoints[r.EndpointID]
	if endpoint == nil {
		return fmt.Errorf("Cannot find endpoint by id: %s", r.EndpointID)
	}
	delete(d.endpoints, r.EndpointID)
	endpoint.hostNic.endpoint = nil
	return nil
}

func (d *HostNicDriver) DiscoverNew(r *network.DiscoveryNotification) error {
	log.Debug("DiscoverNew Called: [ %+v ]", r)
	return nil
}
func (d *HostNicDriver) DiscoverDelete(r *network.DiscoveryNotification) error {
	log.Debug("DiscoverDelete Called: [ %+v ]", r)
	return nil
}
func (d *HostNicDriver) ProgramExternalConnectivity(r *network.ProgramExternalConnectivityRequest) error {
	log.Debug("ProgramExternalConnectivity Called: [ %+v ]", r)
	return nil
}
func (d *HostNicDriver) RevokeExternalConnectivity(r *network.RevokeExternalConnectivityRequest) error {
	log.Debug("RevokeExternalConnectivity Called: [ %+v ]", r)
	return nil
}

func (d *HostNicDriver) findNicFromInterfaces(hardwareAddr string) *HostNic {
	nics, err := net.Interfaces()
	if err == nil {
		for _, nic := range nics {
			if nic.HardwareAddr.String() == hardwareAddr {
				return &HostNic{Name: nic.Name, HardwareAddr: nic.HardwareAddr.String(), Address: GetInterfaceIPAddr(nic)}
			}
		}
	} else {
		log.Error("Get Interfaces error:%s", err.Error())
	}
	return nil
}

func (d *HostNicDriver) findNicFromLinks(hardwareAddr string) *HostNic {
	links, err := netlink.LinkList()
	if err == nil {
		for _, link := range links {
			attr := link.Attrs()
			if attr.HardwareAddr.String() == hardwareAddr {
				return &HostNic{Name: attr.Name, HardwareAddr: attr.HardwareAddr.String()}
			}
		}
	} else {
		log.Error("Get LinkList error:%s", err.Error())
	}
	return nil
}

func (d *HostNicDriver) FindNicByHardwareAddr(hardwareAddr string) *HostNic {
	for _, nic := range d.nics {
		//ensure nic in cache is exist on host.
		if !d.isNicExist(nic.HardwareAddr) {
			log.Info("Delete nic [%+v] to nic talbe", nic)
			delete(d.nics, nic.HardwareAddr)
			continue
		}
		if nic.HardwareAddr == hardwareAddr {
			return nic
		}
	}
	nic := d.findNicFromInterfaces(hardwareAddr)
	if nic == nil {
		nic = d.findNicFromLinks(hardwareAddr)
	}
	if nic != nil {
		log.Info("Add nic [%+v] to nic talbe ", nic)
		d.nics[nic.HardwareAddr] = nic
	}
	return nic
}

// IsNicExist check nic is exist
func (d *HostNicDriver) isNicExist(hardwareAddr string) bool {
	nic := d.findNicFromInterfaces(hardwareAddr)
	if nic == nil {
		nic = d.findNicFromLinks(hardwareAddr)
	}
	return nic != nil
}
