ORCH := python3 orchestration/orchestrator/orchestrator.py
PLAN ?= backlog/plans/todo-tasks.json

.DEFAULT_GOAL := help

.PHONY: help plan load dry-run dashboard status proposals review approve reject retry run reset backend frontend generate check up down logs mongo

# @ 🚀 Help & Info
help: ## Show this beautifully categorized help menu
	@echo ""
	@echo "\033[38;5;244m----------------------------------------------------------------------\033[0m"
	@awk ' \
		BEGIN {FS = ":.*## "}; \
		/^# @/ { printf "\n\033[1;35m%s\033[0m\n", substr($$0, 4) }; \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m \033[37m%s\033[0m\n", $$1, $$2 } \
	' $(MAKEFILE_LIST)
	@echo "\033[38;5;244m----------------------------------------------------------------------\033[0m"
	@echo ""

# @ 🎭 Orchestration & Agents
plan:             ## 📜 Generate plan from story -> backlog/plans/ (no STORY: the latest story, unless already planned)
ifdef STORY
	$(ORCH) plan "$(STORY)"
else
	@story=$$(ls backlog/stories/*.md | grep -v '/TEMPLATE\.md$$' | sort | tail -n 1); \
	if [ -z "$$story" ]; then echo "No story in backlog/stories/. Create one: cp backlog/stories/TEMPLATE.md backlog/stories/s-NN-<slug>.md"; exit 1; fi; \
	plan="backlog/plans/$$(basename "$$story" .md).json"; \
	if [ -e "$$plan" ]; then echo "Latest story $$story is already planned: $$plan"; echo "Queue it with:  make load PLAN=$$plan"; exit 0; fi; \
	echo "Planning latest story: $$story"; \
	$(ORCH) plan "$$story"
endif

load:             ## 📥 Queue tasks from plan JSON
	$(ORCH) load $(PLAN)

dry-run:          ## 🧪 Same flow without calling Claude (run on a throwaway branch)
	$(ORCH) load $(PLAN)
	$(ORCH) run --fake

run:              ## ⚙️  Work on the queue (make run ONLY=S02-01,S02-02 for subset)
	$(ORCH) run $(if $(ONLY),--only $(ONLY))

dashboard:        ## 📊 Live dashboard on http://localhost:8765/orchestration/dashboard/
	$(ORCH) dashboard

status:           ## ℹ️  Show orchestration queue status
	$(ORCH) status

proposals:        ## 💡 View proposed follow-up tasks waiting for your decision
	@$(ORCH) proposals

review:           ## 🗳️  Decide proposals one by one (approve / reject / skip / chat)
	@$(ORCH) review

approve:          ## ✅ Approve proposals (make approve IDS="T-010.1 T-010.3")
	$(ORCH) approve $(IDS)

reject:           ## ❌ Reject proposals (make reject IDS="T-010.2")
	$(ORCH) reject $(IDS)

retry:            ## 🔄 Retry failed tasks of this branch and run their plan (IDS="S02-03" to pick, NORUN=1 to only requeue)
	$(ORCH) retry $(IDS) $(if $(NORUN),,--run)

reset:            ## 🧹 Remove queue state, worktrees and agent/* branches
	$(ORCH) reset

# @ 🖥️  Backend & Services
backend:          ## 🟢 Run API on :8000 against MongoDB (make mongo first; MONGO_URI / MONGO_DB override)
	cd backend && go run ./cmd/server

frontend:         ## 🖼️  Run the Vue app on http://localhost:5173 (proxies /api to make backend)
	cd frontend && npm run dev

up:               ## 🐳 Build and start MongoDB + backend (http://localhost:8000)
	docker compose up -d --build --wait

down:             ## 🛑 Stop the stack (data kept in mongo-data; add -v to drop)
	docker compose down

logs:             ## 📜 Tail docker compose logs live
	docker compose logs -f

mongo:            ## 🍃 Start only MongoDB on localhost:27017
	docker compose up -d --wait mongo

# @ 🛠️  Tooling & Quality Gate
generate:         ## ⚡ Regenerate internal/oas from api/openapi.yaml with ogen
	cd backend && go generate ./...

check:            ## 🔍 Run what quality gate runs: generate drift, gofmt, vet, test (needs MongoDB: make mongo)
	MONGO_TEST_URI=$${MONGO_TEST_URI:-mongodb://localhost:27017} backend/check.sh
