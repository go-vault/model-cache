package pipeline


type ModelComponent struct {
	LibraryName string `json:"library_name,omitempty"`
	ClassName   string `json:"class_name,omitempty"`
}


type ModelIndex struct {
    ClassName         string              `json:"_class_name"`
    DiffusersVersion string              `json:"_diffusers_version,omitempty"`
    Components       map[string][]string `json:"-"`
	ConnectedPipes   []string            `json:"-"`
}


type DownloadOptions struct {
	UseSafetensors   bool
}

