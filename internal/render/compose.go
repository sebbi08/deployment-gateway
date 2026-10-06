package render

// Typed compose structures. Only the fields listed here can ever appear in a
// rendered file; anything else is unrepresentable by construction.

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeService struct {
	Image         string                    `yaml:"image"`
	ContainerName string                    `yaml:"container_name"`
	Hostname      string                    `yaml:"hostname"`
	Command       []qstr                    `yaml:"command,omitempty"`
	Entrypoint    []qstr                    `yaml:"entrypoint,omitempty"`
	Environment   map[string]qstr           `yaml:"environment,omitempty"`
	User          string                    `yaml:"user,omitempty"`
	Restart       string                    `yaml:"restart"`
	CapDrop       []string                  `yaml:"cap_drop"`
	SecurityOpt   []string                  `yaml:"security_opt"`
	ReadOnly      bool                      `yaml:"read_only"`
	Tmpfs         []string                  `yaml:"tmpfs"`
	MemLimit      int64                     `yaml:"mem_limit"`
	MemswapLimit  int64                     `yaml:"memswap_limit"`
	CPUs          float64                   `yaml:"cpus"`
	PidsLimit     int64                     `yaml:"pids_limit"`
	Logging       composeLogging            `yaml:"logging"`
	Volumes       []composeVolume           `yaml:"volumes,omitempty"`
	Ports         []composePort             `yaml:"ports,omitempty"`
	Healthcheck   *composeHealth            `yaml:"healthcheck,omitempty"`
	DependsOn     map[string]composeDepends `yaml:"depends_on,omitempty"`
	Networks      []string                  `yaml:"networks"`
	Labels        map[string]qstr           `yaml:"labels"`
}

type composeLogging struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

type composeVolume struct {
	Type   string      `yaml:"type"`
	Source string      `yaml:"source"`
	Target string      `yaml:"target"`
	Bind   composeBind `yaml:"bind"`
}

type composeBind struct {
	CreateHostPath bool `yaml:"create_host_path"`
}

type composePort struct {
	Target    int    `yaml:"target"`
	Published string `yaml:"published"`
	HostIP    string `yaml:"host_ip"`
	Protocol  string `yaml:"protocol"`
}

type composeHealth struct {
	Test     []qstr `yaml:"test"`
	Interval string `yaml:"interval"`
	Retries  int    `yaml:"retries"`
}

type composeDepends struct {
	Condition string `yaml:"condition"`
}

type composeNetwork struct {
	Name     string `yaml:"name"`
	External bool   `yaml:"external,omitempty"`
}
