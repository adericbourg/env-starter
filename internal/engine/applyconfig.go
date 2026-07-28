package engine

import (
	"fmt"
	"sort"
	"sync"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/logbuf"
)

// reloadPlan is the diff reconciled against live engine state, computed once
// under e.mu at ApplyConfig time. It stores names and config values, never
// *command pointers, and every phase of applyReloadPlan re-checks liveness
// before acting — so a plan computed against a state that has since moved on
// (a user action raced the reload) degrades to a no-op rather than
// misbehaving.
type reloadPlan struct {
	// removedEnvs are environments present in the old config but absent from
	// the new one (R1a). Carries the OLD value so its workflow can be released.
	removedEnvs []config.Environment
	// workflowRestarts are environments whose workflow changed (R2 workflow).
	workflowRestarts []envChange
	// removedCommands are commands present in the old config but absent from
	// the new one (R1b).
	removedCommands []string
	// restartWaves are the non-subsumed changed commands (R3) plus their
	// transitive dependents (R4), laid out in dependency order: every command
	// in one wave must settle before the next wave starts.
	restartWaves [][]string
	// changedSet marks which restartWaves entries need restartCommandWithNewConfig
	// (their own definition changed); everything else is a pure R4 dependent,
	// restarted in place with its existing definition.
	changedSet map[string]struct{}
	// envVarEnvs are environments whose Env map changed but whose workflow did
	// not (R2 env): resolved per command via maybeRestartForEnvChange, not by
	// restarting the whole environment.
	envVarEnvs []envChange
}

// ApplyConfig atomically swaps in a new configuration and reconciles running
// state selectively (see diffConfig/reloadPlan for what "selectively" means).
// It returns an error only when the new config is structurally invalid, in
// which case nothing is mutated. Once it returns nil the new config is in
// force for every reader; the restarts it schedules run asynchronously and
// report progress via Events(), so a restart failure surfaces as CmdError /
// EnvDegraded rather than as an error from this call.
func (e *Engine) ApplyConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("engine: nil config")
	}

	cmdOf, envsOf, envEnvOf, err := buildIndexes(cfg)
	if err != nil {
		return err
	}

	e.mu.Lock()
	diff := diffConfig(e.cfg, cfg)
	plan := e.planReloadLocked(diff, cfg)

	e.cfg = cfg
	e.cmdOf = cmdOf
	e.envsOf = envsOf
	e.envEnvOf = envEnvOf
	for _, env := range cfg.Environments {
		if _, ok := e.envState[env.Name]; !ok {
			e.envState[env.Name] = EnvStopped
		}
	}
	e.mu.Unlock()

	go func() {
		// applyMu serialises plan applications against each other — NOT
		// against the swap above, which always completes immediately so the
		// caller (and the "config changed" banner) never waits on restarts.
		e.applyMu.Lock()
		defer e.applyMu.Unlock()
		e.applyReloadPlan(plan)
	}()

	return nil
}

// planReloadLocked builds a reloadPlan from diff. The caller must hold e.mu;
// it reads live holder sets (via e.commands) to resolve R5 subsumption, and
// newCfg's workflows (not yet installed in e.cfg) to compute dependent waves.
func (e *Engine) planReloadLocked(diff configDiff, newCfg *config.Config) reloadPlan {
	// Environments whose workflow changed are handled wholesale by phase 2:
	// their edges are excluded from the dependent graph (phase 2 already
	// restarts them dependency-ordered via runEnvironment), and a changed
	// command held ONLY by such environments is subsumed by that restart
	// rather than restarted individually.
	workflowChangedEnvs := make(map[string]struct{})
	var workflowRestarts []envChange
	var envVarEnvs []envChange
	for name, change := range diff.changedEnvs {
		switch {
		case change.kinds&envChangedWorkflow != 0:
			workflowChangedEnvs[name] = struct{}{}
			workflowRestarts = append(workflowRestarts, change)
		case change.kinds&envChangedEnv != 0:
			envVarEnvs = append(envVarEnvs, change)
		}
	}

	removalEnvs := make(map[string]struct{}, len(diff.removedEnvs)+len(workflowChangedEnvs))
	for _, env := range diff.removedEnvs {
		removalEnvs[env.Name] = struct{}{}
	}
	for name := range workflowChangedEnvs {
		removalEnvs[name] = struct{}{}
	}

	var seeds []string
	changedSet := make(map[string]struct{})
	for _, name := range diff.changedCommands {
		c, ok := e.commands[name]
		if !ok || len(c.holders) == 0 {
			continue // not currently running/held — nothing to restart
		}
		subsumed := true
		for h := range c.holders {
			if _, inRemoval := removalEnvs[h]; !inRemoval {
				subsumed = false
				break
			}
		}
		if subsumed {
			continue
		}
		seeds = append(seeds, name)
		changedSet[name] = struct{}{}
	}
	sort.Strings(seeds)

	return reloadPlan{
		removedEnvs:      diff.removedEnvs,
		workflowRestarts: workflowRestarts,
		removedCommands:  diff.removedCommands,
		restartWaves:     computeDependentWaves(newCfg, seeds, workflowChangedEnvs),
		changedSet:       changedSet,
		envVarEnvs:       envVarEnvs,
	}
}

// computeDependentWaves returns seeds plus their transitive dependents, laid
// out in dependency order (each wave must settle before the next starts).
// Edges are unioned across every environment in cfg except those named in
// excludeEnvs (already handled wholesale by an environment-level restart).
//
// Cross-environment dependency cycles are possible even though config
// validation forbids a cycle within a single workflow: env A can declare "x
// depends-on y" while env B declares "y depends-on x". Kahn's algorithm
// handles this by emitting whatever nodes never reach in-degree 0 as one
// final, unordered wave, instead of deadlocking like a channel-based
// scheduler (e.g. runEnvironment's) would.
func computeDependentWaves(cfg *config.Config, seeds []string, excludeEnvs map[string]struct{}) [][]string {
	if len(seeds) == 0 {
		return nil
	}

	// forward[dep] is the set of commands that declare dep as a dependency —
	// i.e. dep's dependents, which must restart after dep does.
	forward := make(map[string]map[string]struct{})
	for _, env := range cfg.Environments {
		if _, excluded := excludeEnvs[env.Name]; excluded {
			continue
		}
		for _, step := range env.Workflow {
			for _, dep := range step.DependsOn {
				if forward[dep] == nil {
					forward[dep] = make(map[string]struct{})
				}
				forward[dep][step.Command] = struct{}{}
			}
		}
	}

	// Forward-BFS from seeds to find every transitive dependent.
	affected := make(map[string]struct{}, len(seeds))
	queue := make([]string, len(seeds))
	copy(queue, seeds)
	for _, s := range seeds {
		affected[s] = struct{}{}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for dependent := range forward[n] {
			if _, ok := affected[dependent]; ok {
				continue
			}
			affected[dependent] = struct{}{}
			queue = append(queue, dependent)
		}
	}

	// Kahn's algorithm over the subgraph induced by affected, with edges
	// restricted to dep->dependent pairs where both ends are affected.
	inDegree := make(map[string]int, len(affected))
	for n := range affected {
		inDegree[n] = 0
	}
	for dep, dependents := range forward {
		if _, ok := affected[dep]; !ok {
			continue
		}
		for dependent := range dependents {
			if _, ok := affected[dependent]; ok {
				inDegree[dependent]++
			}
		}
	}

	var waves [][]string
	for len(inDegree) > 0 {
		var wave []string
		for n, d := range inDegree {
			if d == 0 {
				wave = append(wave, n)
			}
		}
		if len(wave) == 0 {
			// A cycle across environments: nothing has in-degree 0. Emit
			// everything left as one final wave rather than looping forever.
			for n := range inDegree {
				wave = append(wave, n)
			}
		}
		sort.Strings(wave) // deterministic order
		waves = append(waves, wave)
		for _, n := range wave {
			delete(inDegree, n)
			for dependent := range forward[n] {
				if _, ok := inDegree[dependent]; ok {
					inDegree[dependent]--
				}
			}
		}
	}
	return waves
}

// applyReloadPlan executes a reloadPlan's six phases in order:
//  1. stop removed environments
//  2. restart workflow-changed environments (parallel across environments)
//  3. purge removed commands
//  4. restart command waves for R3/R4 (waves sequential, within-wave parallel)
//  5. propagate env-map-only changes (R2 env)
//  6. final environment-state sweep
//
// Phases 1-2 run before 3-4 because both change holder sets, which changes
// effectiveEnv; 4 runs before 5 because a command fully restarted in phase 4
// gets a fresh appliedEnv, making phase 5's maybeRestartForEnvChange a correct
// no-op for it (reversed, it would restart twice).
func (e *Engine) applyReloadPlan(plan reloadPlan) {
	var wg sync.WaitGroup

	for _, oldEnv := range plan.removedEnvs {
		wg.Add(1)
		go func(oldEnv config.Environment) {
			defer wg.Done()
			e.reloadStopRemovedEnv(oldEnv)
		}(oldEnv)
	}
	wg.Wait()

	wg = sync.WaitGroup{}
	for _, change := range plan.workflowRestarts {
		wg.Add(1)
		go func(change envChange) {
			defer wg.Done()
			e.reloadRestartEnvWorkflow(change)
		}(change)
	}
	wg.Wait()

	for _, name := range plan.removedCommands {
		e.reloadPurgeCommand(name)
	}

	for _, wave := range plan.restartWaves {
		wg = sync.WaitGroup{}
		for _, name := range wave {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				e.reloadRestartWaveMember(name, plan.changedSet)
			}(name)
		}
		wg.Wait()
	}

	for _, change := range plan.envVarEnvs {
		e.reloadPropagateEnvChange(change)
	}

	e.reloadFinalSweep()
}

// reloadStopRemovedEnv implements R1a for one environment: releases its
// holds (if it was active) and drops its envState entry entirely.
func (e *Engine) reloadStopRemovedEnv(oldEnv config.Environment) {
	if e.isEnvActive(oldEnv.Name) {
		e.setEnvState(oldEnv.Name, EnvStopping)
		e.releaseWorkflow(oldEnv)
		e.setEnvState(oldEnv.Name, EnvStopped)
	}
	e.mu.Lock()
	delete(e.envState, oldEnv.Name)
	e.mu.Unlock()
}

// reloadRestartEnvWorkflow implements the workflow branch of R2: releases the
// OLD workflow's holds, then runs the NEW workflow. A command still held by
// another, unaffected environment is not torn down by the release
// (releaseCommand's refcount semantics), and acquireAndStart recreates a
// fully-released command fresh from the already-swapped cmdOf — so this
// naturally applies any command definition change too (R5 subsumption).
// A no-op for an environment that is not currently active: the new workflow
// simply applies the next time it is started.
func (e *Engine) reloadRestartEnvWorkflow(change envChange) {
	if !e.isEnvActive(change.old.Name) {
		return
	}
	e.setEnvState(change.new.Name, EnvStarting)
	e.releaseWorkflow(change.old)
	e.runEnvironment(change.new)
}

// reloadPurgeCommand implements R1b for one command: releases every
// remaining holder (defensive — validation plus phases 1-2 should already
// have released every holder of a command no longer referenced by any
// environment) and forgets it entirely.
func (e *Engine) reloadPurgeCommand(name string) {
	e.mu.Lock()
	c, ok := e.commands[name]
	var holderNames []string
	if ok {
		for h := range c.holders {
			holderNames = append(holderNames, h)
		}
	}
	e.mu.Unlock()
	if !ok {
		return
	}
	for _, h := range holderNames {
		e.releaseCommand(h, name)
	}
	e.mu.Lock()
	delete(e.commands, name)
	e.mu.Unlock()
}

// reloadRestartWaveMember restarts one command from a restartWaves entry: a
// full restart under the new definition if it is in changedSet (R3), or a
// plain in-place restart otherwise (R4 — a dependent whose own definition did
// not change but must still cycle after the thing it depends on). A no-op if
// the command is no longer running/held by the time this runs.
func (e *Engine) reloadRestartWaveMember(name string, changedSet map[string]struct{}) {
	e.mu.Lock()
	c, ok := e.commands[name]
	newCfg := e.cmdOf[name]
	e.mu.Unlock()
	if !ok || len(c.holders) == 0 {
		return
	}
	if _, changed := changedSet[name]; changed {
		e.restartCommandWithNewConfig(c, newCfg)
		return
	}
	e.restartCommandInPlace(c)
}

// reloadPropagateEnvChange implements the env branch of R2: for each command
// in the environment's (unchanged) workflow, restart it only if its
// effective merged env actually changed (maybeRestartForEnvChange) — never
// the whole environment. A no-op for an environment that is not active.
func (e *Engine) reloadPropagateEnvChange(change envChange) {
	if !e.isEnvActive(change.new.Name) {
		return
	}
	for _, step := range change.new.Workflow {
		e.mu.Lock()
		c, ok := e.commands[step.Command]
		e.mu.Unlock()
		if !ok {
			continue
		}
		e.maybeRestartForEnvChange(c, change.new.Name, "reloaded")
	}
}

// reloadFinalSweep recomputes rollup state for every currently active
// environment, guaranteeing a consistent terminal state even if some restart
// above failed.
func (e *Engine) reloadFinalSweep() {
	e.mu.Lock()
	envs := append([]config.Environment(nil), e.cfg.Environments...)
	e.mu.Unlock()
	for _, env := range envs {
		if e.isEnvActive(env.Name) {
			e.recomputeEnvState(env)
		}
	}
}

// restartCommandWithNewConfig restarts a live command with a changed
// definition, applying newCfg to every environment currently holding it.
// Unlike restartCommandInPlace/relaunch (which reuse c.runDir and skip
// setup), this performs a full managed start under the new definition —
// re-fetching the source and re-running setup — because the change that
// triggered this restart may be exactly a changed source or setup. Holders
// are untouched.
//
// Teardown runs against the OLD definition (c.cfg is only swapped for newCfg
// after teardownForRestart returns), so a changed teardown script never
// applies to the process it is meant to be cleaning up after.
func (e *Engine) restartCommandWithNewConfig(c *command, newCfg config.Command) {
	// Cancel the current owner goroutine, wait for any in-flight launch to
	// settle, then wait for the monitor goroutine itself to have returned.
	// The startDone wait alone is not enough: the monitor goroutine
	// (superviseService/superviseTask) can still be about to read c.cfg/
	// c.policy in a fresh loop iteration even after quit is closed, and
	// c.cfg/c.policy are about to be mutated below. monitorDone is nil when
	// this command never had a monitor (e.g. a task with no liveness check),
	// in which case there is nothing to wait for.
	e.mu.Lock()
	c.userStopped = true
	if c.quit != nil {
		c.quitOnce.Do(func() { close(c.quit) })
	}
	startDone := c.startDone
	monitorDone := c.monitorDone
	e.mu.Unlock()
	<-startDone
	if monitorDone != nil {
		<-monitorDone
	}

	// Reset cancellation and retry state for a fresh supervise cycle.
	e.mu.Lock()
	c.userStopped = false
	c.quit = nil
	c.quitOnce = sync.Once{}
	c.retries = 0
	e.mu.Unlock()

	e.setCmdState(newCfg.Name, CmdRestarting, nil)
	e.teardownForRestart(c) // reads c.cfg.Teardown — must run before the swap below.

	// Swap in the new definition and a fresh start barrier. Everything from
	// here on (fetchSource, setup, launch, readiness) reads the new cfg.
	e.mu.Lock()
	c.cfg = newCfg
	c.policy = resolveRestart(newCfg)
	c.startDone = make(chan struct{})
	e.mu.Unlock()

	e.setCmdState(newCfg.Name, CmdStarting, nil)

	logPath := e.logPath(newCfg.Name)
	file, err := logbuf.OpenFile(logPath)
	if err != nil {
		file = nil
	}
	e.mu.Lock()
	c.writer = logbuf.NewWriter(c.ring, file)
	e.mu.Unlock()

	// doStart fetches the source, runs setup, launches the process, then
	// hands off to handleTask/handleService, which set the terminal state
	// and start monitoring themselves.
	e.doStart(c)
	close(c.startDone)

	// Recompute state for every env sharing this command so their rollup
	// state reflects the restart outcome (mirrors restartCommandInPlace).
	e.recomputeEnvsFor(newCfg.Name)
}
