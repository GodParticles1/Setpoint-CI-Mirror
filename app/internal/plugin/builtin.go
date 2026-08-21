package plugin

type MetadataDescriptor struct {
	metadata Metadata
}

func NewMetadataDescriptor(metadata Metadata) MetadataDescriptor {
	return MetadataDescriptor{metadata: cloneMetadata(metadata)}
}

func (candidate MetadataDescriptor) Metadata() Metadata {
	return cloneMetadata(candidate.metadata)
}

func DevelopmentChecks() []CheckDescriptor {
	return []CheckDescriptor{
		NewMetadataDescriptor(Metadata{
			ID:               "dev.system-info",
			Category:         "development",
			Name:             "System information metadata",
			Version:          "0.1.0",
			Description:      "Development-only metadata entry; it performs no check or mutation.",
			Mode:             ModeReadOnly,
			Risk:             RiskLow,
			Impact:           "none; metadata registration only",
			SupportedSystems: []string{"linux"},
			Parameters:       []Parameter{},
		}),
	}
}
