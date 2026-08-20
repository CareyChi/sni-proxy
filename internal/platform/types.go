package platform

type InitSystem string

const (
	InitUnknown InitSystem = "unknown"
	InitSystemd InitSystem = "systemd"
	InitOpenRC  InitSystem = "openrc"
	InitSysV    InitSystem = "sysvinit"
	InitRunit   InitSystem = "runit"
)

type ContainerInfo struct {
	Detected bool   `json:"detected"`
	Type     string `json:"type,omitempty"`
}

type Info struct {
	OS                  string        `json:"os"`
	Distribution        string        `json:"distribution"`
	DistributionID      string        `json:"distribution_id"`
	DistributionVersion string        `json:"distribution_version"`
	IDLike              []string      `json:"id_like,omitempty"`
	Architecture        string        `json:"architecture"`
	Kernel              string        `json:"kernel"`
	InitSystem          InitSystem    `json:"init_system"`
	ServiceManager      string        `json:"service_manager"`
	PackageManager      string        `json:"package_manager"`
	InstallDir          string        `json:"install_dir"`
	Compatibility       string        `json:"compatibility"`
	Container           ContainerInfo `json:"container"`
}
