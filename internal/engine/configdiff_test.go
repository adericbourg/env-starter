package engine

import (
	"testing"

	"github.com/adericbourg/env-starter/internal/config"
)

func TestDiffConfig_ofIdenticalConfigs_isEmpty(t *testing.T) {
	// Given two configs with identical content
	old := &config.Config{
		Commands:     []config.Command{{Name: "db", Run: "run db"}},
		Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{{Command: "db"}}}},
	}
	new := &config.Config{
		Commands:     []config.Command{{Name: "db", Run: "run db"}},
		Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{{Command: "db"}}}},
	}

	// When diffing them
	diff := diffConfig(old, new)

	// Then nothing is reported changed, removed, or added
	if len(diff.removedCommands) != 0 || len(diff.changedCommands) != 0 || len(diff.removedEnvs) != 0 || len(diff.changedEnvs) != 0 {
		t.Fatalf("expected empty diff, got %+v", diff)
	}
}

func TestDiffConfig_whenCommandRemoved_listsItRemoved(t *testing.T) {
	// Given a command present in old but absent from new
	old := &config.Config{Commands: []config.Command{{Name: "db", Run: "run db"}, {Name: "app", Run: "run app"}}}
	new := &config.Config{Commands: []config.Command{{Name: "app", Run: "run app"}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then "db" is reported removed
	if len(diff.removedCommands) != 1 || diff.removedCommands[0] != "db" {
		t.Fatalf("expected removedCommands=[db], got %v", diff.removedCommands)
	}
	if len(diff.changedCommands) != 0 {
		t.Fatalf("expected no changed commands, got %v", diff.changedCommands)
	}
}

func TestDiffConfig_whenCommandRunChanged_listsItChanged(t *testing.T) {
	// Given a command whose Run field differs between old and new
	old := &config.Config{Commands: []config.Command{{Name: "db", Run: "run old"}}}
	new := &config.Config{Commands: []config.Command{{Name: "db", Run: "run new"}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then "db" is reported changed, not removed
	if len(diff.changedCommands) != 1 || diff.changedCommands[0] != "db" {
		t.Fatalf("expected changedCommands=[db], got %v", diff.changedCommands)
	}
	if len(diff.removedCommands) != 0 {
		t.Fatalf("expected no removed commands, got %v", diff.removedCommands)
	}
}

func TestDiffConfig_whenCommandReadinessPointerChanged_listsItChanged(t *testing.T) {
	// Given a command whose Readiness pointer differs in pointed-to value
	// (guards that comparison follows the pointer rather than comparing addresses)
	old := &config.Config{Commands: []config.Command{{Name: "db", Run: "run db", Readiness: &config.Readiness{TCP: "localhost:5432"}}}}
	new := &config.Config{Commands: []config.Command{{Name: "db", Run: "run db", Readiness: &config.Readiness{TCP: "localhost:5433"}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then "db" is reported changed
	if len(diff.changedCommands) != 1 || diff.changedCommands[0] != "db" {
		t.Fatalf("expected changedCommands=[db], got %v", diff.changedCommands)
	}
}

func TestDiffConfig_ofEnvironmentDescriptionOnly_marksCosmeticOnly(t *testing.T) {
	// Given an environment whose Description differs, nothing else
	old := &config.Config{Environments: []config.Environment{{Name: "env", Description: "old desc", Workflow: []config.WorkflowStep{{Command: "db"}}}}}
	new := &config.Config{Environments: []config.Environment{{Name: "env", Description: "new desc", Workflow: []config.WorkflowStep{{Command: "db"}}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is reported changed with only the cosmetic bit set
	change, ok := diff.changedEnvs["env"]
	if !ok {
		t.Fatalf("expected env to be reported changed")
	}
	if change.kinds != envChangedCosmetic {
		t.Fatalf("expected only envChangedCosmetic, got %v", change.kinds)
	}
}

func TestDiffConfig_ofEnvironmentAutoStartOnly_marksCosmeticOnly(t *testing.T) {
	// Given an environment whose AutoStart differs, nothing else
	old := &config.Config{Environments: []config.Environment{{Name: "env", AutoStart: false, Workflow: []config.WorkflowStep{{Command: "db"}}}}}
	new := &config.Config{Environments: []config.Environment{{Name: "env", AutoStart: true, Workflow: []config.WorkflowStep{{Command: "db"}}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is reported changed with only the cosmetic bit set
	change, ok := diff.changedEnvs["env"]
	if !ok {
		t.Fatalf("expected env to be reported changed")
	}
	if change.kinds != envChangedCosmetic {
		t.Fatalf("expected only envChangedCosmetic, got %v", change.kinds)
	}
}

func TestDiffConfig_ofEnvironmentEnvMap_marksEnvChange(t *testing.T) {
	// Given an environment whose Env map differs
	old := &config.Config{Environments: []config.Environment{{Name: "env", Env: map[string]string{"K": "old"}, Workflow: []config.WorkflowStep{{Command: "db"}}}}}
	new := &config.Config{Environments: []config.Environment{{Name: "env", Env: map[string]string{"K": "new"}, Workflow: []config.WorkflowStep{{Command: "db"}}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is reported changed with the env bit set
	change, ok := diff.changedEnvs["env"]
	if !ok {
		t.Fatalf("expected env to be reported changed")
	}
	if change.kinds != envChangedEnv {
		t.Fatalf("expected only envChangedEnv, got %v", change.kinds)
	}
}

func TestDiffConfig_ofWorkflowStepAdded_marksWorkflowChange(t *testing.T) {
	// Given an environment with an added workflow step
	old := &config.Config{Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{{Command: "db"}}}}}
	new := &config.Config{Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{{Command: "db"}, {Command: "migrate"}}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is reported changed with the workflow bit set
	change, ok := diff.changedEnvs["env"]
	if !ok {
		t.Fatalf("expected env to be reported changed")
	}
	if change.kinds != envChangedWorkflow {
		t.Fatalf("expected only envChangedWorkflow, got %v", change.kinds)
	}
}

func TestDiffConfig_ofDependsOnChanged_marksWorkflowChange(t *testing.T) {
	// Given a workflow step whose depends-on list differs
	old := &config.Config{Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{
		{Command: "db"}, {Command: "migrate"},
	}}}}
	new := &config.Config{Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{
		{Command: "db"}, {Command: "migrate", DependsOn: []string{"db"}},
	}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is reported changed with the workflow bit set
	change, ok := diff.changedEnvs["env"]
	if !ok {
		t.Fatalf("expected env to be reported changed")
	}
	if change.kinds != envChangedWorkflow {
		t.Fatalf("expected only envChangedWorkflow, got %v", change.kinds)
	}
}

func TestDiffConfig_ofEnvAndWorkflowChanged_marksBothKinds(t *testing.T) {
	// Given an environment whose Env map AND workflow both differ
	old := &config.Config{Environments: []config.Environment{{Name: "env", Env: map[string]string{"K": "old"}, Workflow: []config.WorkflowStep{{Command: "db"}}}}}
	new := &config.Config{Environments: []config.Environment{{Name: "env", Env: map[string]string{"K": "new"}, Workflow: []config.WorkflowStep{{Command: "db"}, {Command: "migrate"}}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then both the env and workflow bits are set
	change, ok := diff.changedEnvs["env"]
	if !ok {
		t.Fatalf("expected env to be reported changed")
	}
	if change.kinds&envChangedEnv == 0 || change.kinds&envChangedWorkflow == 0 {
		t.Fatalf("expected both envChangedEnv and envChangedWorkflow, got %v", change.kinds)
	}
}

func TestDiffConfig_whenEnvironmentRemoved_carriesItsOldWorkflow(t *testing.T) {
	// Given an environment present in old but absent from new
	oldEnv := config.Environment{Name: "env", Workflow: []config.WorkflowStep{{Command: "db"}}}
	old := &config.Config{Environments: []config.Environment{oldEnv}}
	new := &config.Config{}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is reported removed, carrying its old workflow
	if len(diff.removedEnvs) != 1 {
		t.Fatalf("expected one removed env, got %v", diff.removedEnvs)
	}
	if len(diff.removedEnvs[0].Workflow) != 1 || diff.removedEnvs[0].Workflow[0].Command != "db" {
		t.Fatalf("expected removed env to carry its old workflow, got %+v", diff.removedEnvs[0])
	}
}

func TestDiffConfig_whenEnvironmentAdded_listsNothingToRestart(t *testing.T) {
	// Given an environment present in new but absent from old
	old := &config.Config{}
	new := &config.Config{Environments: []config.Environment{{Name: "env", Workflow: []config.WorkflowStep{{Command: "db"}}}}}

	// When diffing them
	diff := diffConfig(old, new)

	// Then it is neither removed nor changed (adding is not a restart trigger)
	if len(diff.removedEnvs) != 0 {
		t.Fatalf("expected no removed envs, got %v", diff.removedEnvs)
	}
	if _, ok := diff.changedEnvs["env"]; ok {
		t.Fatalf("expected added env not to appear in changedEnvs")
	}
}
