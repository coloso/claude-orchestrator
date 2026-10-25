package memory_test

import (
	"testing"

	"orch-test/backend/internal/catalog/adapter/memory"
	"orch-test/backend/internal/catalog/adapter/repotest"
	"orch-test/backend/internal/catalog/domain"
)

func TestRepositoryContract(t *testing.T) {
	repotest.Run(t, func(*testing.T) domain.Repository { return memory.NewRepository() })
}
