# flow-workspace-roles Specification

## Purpose
TBD - created by archiving change workspace-analyst-role. Update Purpose after archive.

## Requirements

### Requirement: Workspace analyst role and scope mapping (PRD §10.1)
The platform SHALL support the `analyst` workspace role alongside `owner`, `admin`, `editor`, and `viewer` in organization invitations, member role updates, member listing, and `ScopesForRole` authorization checks.

#### Scenario: Invite and assign analyst role
- **WHEN** a workspace admin invites a user with role `analyst` or updates an existing member's role to `analyst`
- **THEN** `ListMembers` and `ResolveScope` SHALL report the member's workspace role as `analyst`
- **AND** `ScopesForRole("analyst")` SHALL grant analysis, collection, monitor, schedule, report, and MCP execution scopes while denying workspace admin operations (`CreateInvite`, `SetMemberRole`, `RemoveMember`)
