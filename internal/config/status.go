package config

// Status holds the status page configuration
type Status struct {
	Listen string `yaml:"listen,omitempty" json:"listen,omitempty" validate:"required"`
}
