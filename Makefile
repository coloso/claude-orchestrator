ORCH := python3 orchestration/orchestrator/orchestrator.py
PLAN ?= orchestration/plans/todo-tasks.json

.PHONY: plan load demo dry-run status proposals approve reject retry run reset backend generate check up down logs mongo test-mongo

plan:             ## make plan STORY=stories/S02-shopping-list.md -> orchestration/plans/S02-shopping-list.json (review it!)
	$(ORCH) plan $(STORY)

load:             ## make load PLAN=orchestration/plans/S02-shopping-list.json -> queue its tasks
	$(ORCH) load $(PLAN)

demo:             ## queue the prepared todo-API plan and run it with real Claude agents
	$(ORCH) load $(PLAN)
	$(ORCH) run

dry-run:          ## same flow without calling Claude (run it on a throwaway branch)
	$(ORCH) load $(PLAN)
	$(ORCH) run --fake

status:
	$(ORCH) status

proposals:        ## follow-up tasks agents proposed (e.g. security fixes), waiting for your decision
	$(ORCH) proposals

approve:          ## make approve IDS="T-010.1 T-010.3"; then make run
	$(ORCH) approve $(IDS)

reject:           ## make reject IDS="T-010.2"
	$(ORCH) reject $(IDS)

retry:            ## make retry IDS="S02-03" -> failed tasks back to pending with fresh attempts
	$(ORCH) retry $(IDS)

run:              ## work on the queue; make run ONLY=S02-01,S02-02 to run just those tasks
	$(ORCH) run $(if $(ONLY),--only $(ONLY))

reset:            ## remove queue state, worktrees and agent/* branches
	$(ORCH) reset

backend:          ## run the API on :8000 with in-memory storage (STORAGE=mongo to use the compose MongoDB)
	cd backend && go run ./cmd/server

up:               ## build and start MongoDB + backend (http://localhost:8000)
	docker compose up -d --build --wait

down:             ## stop the stack (data is kept in the mongo-data volume; add -v to drop it)
	docker compose down

logs:
	docker compose logs -f

mongo:            ## start only MongoDB on localhost:27017 (for local runs and test-mongo)
	docker compose up -d --wait mongo

test-mongo: mongo ## run all backend checks including the MongoDB adapter tests
	MONGO_TEST_URI=mongodb://localhost:27017 backend/check.sh

generate:         ## regenerate internal/oas from api/openapi.yaml with ogen
	cd backend && go generate ./...

check:            ## what the quality gate runs: generate drift, gofmt, vet, test
	backend/check.sh
