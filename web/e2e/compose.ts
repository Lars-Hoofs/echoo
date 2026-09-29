// Every spec that seeds data with SQL goes through this helper, so the target stack is always an
// explicit compose project. Without -p, `docker compose` falls back to the default project of the
// working directory and would write into whatever stack a developer has running there.
import { execFileSync } from 'node:child_process'

const project = process.env.ECHOO_E2E_COMPOSE_PROJECT ?? ''

if (project === '') {
  throw new Error('ECHOO_E2E_COMPOSE_PROJECT is required: set it to the docker compose project name of the stack under test (for example echoo-e2e).')
}
if (project === 'echoo' && process.env.ECHOO_E2E_ALLOW_DEFAULT_PROJECT !== '1') {
  throw new Error('ECHOO_E2E_COMPOSE_PROJECT=echoo is the default project name and usually a stack with real data. Use a dedicated project, or set ECHOO_E2E_ALLOW_DEFAULT_PROJECT=1 to allow it.')
}

export const composeProject = project

const repoRoot = new URL('../..', import.meta.url).pathname

// Runs psql inside the db service of the stack under test; psqlArgs are appended after the
// connection flags (for example ['-At', '-c', statement]).
export function dockerPsql(psqlArgs: string[]): string {
  return execFileSync('docker', ['compose', '-p', composeProject, 'exec', '-T', 'db', 'psql', '-U', 'postgres', '-d', 'echoo', ...psqlArgs], {
    cwd: repoRoot,
    stdio: 'pipe',
    encoding: 'utf8',
  }).trim()
}
