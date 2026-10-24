ORCH := python3 orchestration/orchestrator/orchestrator.py
PLAN := orchestration/plans/todo-tasks.json

.PHONY: demo dry-run status reset backend generate check

demo:             ## queue the prepared todo-API plan and run it with real Claude agents
	$(ORCH) load $(PLAN)
	$(ORCH) run

dry-run:          ## same flow without calling Claude (run it on a throwaway branch)
	$(ORCH) load $(PLAN)
	$(ORCH) run --fake

status:
	$(ORCH) status

reset:            ## remove queue state, worktrees and agent/* branches
	$(ORCH) reset

backend:          ## run the API on :8000
	cd backend && go run ./cmd/server

generate:         ## regenerate internal/oas from api/openapi.yaml with ogen
	cd backend && go generate ./...

check:            ## what the quality gate runs: generate drift, gofmt, vet, test
	backend/check.sh
