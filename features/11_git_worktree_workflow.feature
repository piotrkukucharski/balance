Feature: Git Worktree Multi-Environment Workflow
  As a developer working on Balance
  I want to create isolated git worktrees with dedicated ports and database copies
  So that I can develop and test features concurrently without database locks or port collisions

  Scenario: Creating an isolated worktree with a dedicated port and data directory
    Given the repository is on the main worktree
    When I run task dev:worktree:create with name "feature-billing"
    Then a new git worktree is created in "../development/feature-billing" on branch "feature-billing"
    And the worktree has a ".env" file with a dedicated port between 10000 and 11000
    And the worktree has an independent copy of the database directory

  Scenario: Preventing dev:worktree:update:maintree execution from within a secondary worktree
    Given I am inside a secondary worktree "feature-billing"
    When I run task dev:worktree:update:maintree with name "feature-billing"
    Then the task should fail with an error indicating it can only be run from the maintree

  Scenario: Updating the worktree branch with latest main commits
    Given I am inside a secondary worktree "feature-billing"
    And main tree has new commits
    When I run task dev:worktree:update:branch
    Then the current worktree branch is rebased on top of main

  Scenario: Updating maintree from worktree via fast-forward
    Given I am in the main repository tree
    And the worktree branch "feature-billing" has completed commits
    When I run task dev:worktree:update:maintree with name "feature-billing"
    Then the main branch is updated via fast-forward to match "feature-billing"

  Scenario: Clearing a worktree preserves or removes branch based on flags
    Given a worktree exists at "../development/feature-billing"
    When I run task dev:worktree:clear with name "feature-billing" without delete-branch flag
    Then the worktree directory is removed but the git branch "feature-billing" is preserved
    When I run task dev:worktree:clear with name "feature-billing" with delete-branch flag
    Then the git branch "feature-billing" is removed
