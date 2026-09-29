// A minimal SMTP server that keeps every message in memory, for e2e/invites.spec.ts.
//
//   node e2e/smtp-sink.ts <smtp-port> <http-port> <cert-file>
//
// It speaks SMTP over implicit TLS with a self-signed certificate for host.docker.internal
// (written to <cert-file>, which the compose override mounts into the Echoo container as its
// trust root) and serves the captured messages as JSON on http://127.0.0.1:<http-port>/messages.
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { type TLSSocket, createServer as createTLSServer } from 'node:tls'

interface Captured {
  from: string
  to: string[]
  raw: string
}

const [smtpPort, httpPort, certFile] = process.argv.slice(2)
if (!smtpPort || !httpPort || !certFile) throw new Error('usage: smtp-sink.ts <smtp-port> <http-port> <cert-file>')

mkdirSync(dirname(certFile), { recursive: true })
const dir = mkdtempSync(join(tmpdir(), 'smtp-sink-'))
const keyFile = join(dir, 'key.pem')
execFileSync('openssl', [
  ...['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-days', '2'],
  ...['-keyout', keyFile, '-out', certFile, '-subj', '/CN=host.docker.internal', '-addext', 'subjectAltName=DNS:host.docker.internal'],
])

const messages: Captured[] = []

function serve(socket: TLSSocket) {
  let buffer = ''
  let data: string | null = null
  let from = ''
  let to: string[] = []
  const reply = (line: string) => socket.write(line + '\r\n')
  reply('220 sink ESMTP')

  const command = (line: string) => {
    const verb = line.slice(0, 4).toUpperCase()
    if (verb === 'EHLO' || verb === 'HELO') {
      socket.write('250-sink\r\n250-AUTH PLAIN\r\n250 8BITMIME\r\n')
    } else if (verb === 'AUTH') {
      reply('235 ok')
    } else if (verb === 'MAIL') {
      from = /<([^>]*)>/.exec(line)?.[1] ?? ''
      to = []
      reply('250 ok')
    } else if (verb === 'RCPT') {
      to.push(/<([^>]*)>/.exec(line)?.[1] ?? '')
      reply('250 ok')
    } else if (verb === 'DATA') {
      data = ''
      reply('354 go ahead')
    } else if (verb === 'RSET' || verb === 'NOOP') {
      reply('250 ok')
    } else if (verb === 'QUIT') {
      reply('221 bye')
      socket.end()
    } else {
      reply('502 not implemented')
    }
  }

  socket.on('data', (chunk: Buffer) => {
    buffer += chunk.toString('latin1')
    for (;;) {
      if (data !== null) {
        const end = buffer.indexOf('\r\n.\r\n')
        if (end < 0) return
        // Undo dot-stuffing (RFC 5321 4.5.2).
        data += buffer.slice(0, end).replace(/^\.\./gm, '.')
        buffer = buffer.slice(end + 5)
        messages.push({ from, to, raw: data })
        data = null
        reply('250 queued')
        continue
      }
      const eol = buffer.indexOf('\r\n')
      if (eol < 0) return
      const line = buffer.slice(0, eol)
      buffer = buffer.slice(eol + 2)
      command(line)
    }
  })
  socket.on('error', () => {
    // A client that hangs up mid-conversation is not the sink's problem.
  })
}

createTLSServer({ key: readFileSync(keyFile), cert: readFileSync(certFile) }, serve).listen(Number(smtpPort), '0.0.0.0')

createServer((req, res) => {
  if (req.method === 'DELETE') messages.length = 0
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(messages))
}).listen(Number(httpPort), '127.0.0.1')

writeFileSync(join(dir, 'ready'), '')
console.log(`smtp sink ready: smtps on :${smtpPort}, messages on http://127.0.0.1:${httpPort}/messages`)
