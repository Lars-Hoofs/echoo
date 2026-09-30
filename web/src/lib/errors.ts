import { ApiError } from './api'
import type { ProbeResult } from './mailbox'

const messages: Record<string, string> = {
  network: 'Geen verbinding met Echoo. Controleer je netwerk en probeer het opnieuw.',
  internal: 'Er ging iets mis aan de kant van Echoo. Probeer het opnieuw; blijft het misgaan, meld het dan bij je beheerder.',
  invalid_credentials: 'Inloggen mislukt. Controleer je e-mailadres en wachtwoord, of probeer het over 15 minuten opnieuw.',
  invalid_code: 'Deze code klopt niet of is al gebruikt.',
  rate_limited: 'Te veel pogingen. Wacht een minuut en probeer het opnieuw.',
  csrf_failed: 'Je sessie is verlopen. Vernieuw de pagina.',
  unauthenticated: 'Je bent uitgelogd. Log opnieuw in.',
  forbidden: 'Je hebt geen rechten voor deze actie.',
  token_read_only: 'Dit token mag alleen lezen.',
  session_required: 'Log in via de browser om dit te doen.',
  not_found: 'Dit bestaat niet (meer).',
  validation_failed: 'Controleer de gemarkeerde velden.',
  version_conflict: 'Dit gesprek is intussen door iemand anders gewijzigd. De lijst is vernieuwd.',
  assignee_no_access: 'Deze persoon heeft geen schrijfrechten op de mailbox van dit gesprek.',
  team_no_access: 'Dit team heeft geen toegang tot de mailbox van dit gesprek.',
  not_snoozable: 'Gesloten gesprekken en spam kun je niet uitstellen.',
  mfa_already_enabled: 'Tweestapsverificatie staat al aan.',
  too_late: 'Te laat: het bericht wordt al verzonden en kan niet meer worden teruggehaald.',
  mention_no_access: 'Je hebt iemand genoemd die geen toegang heeft tot deze mailbox. Verwijder de vermelding.',
  file_too_large: 'Dit bestand is groter dan de toegestane bijlagegrootte.',
  attachment_infected: 'De virusscanner heeft in dit bestand een virus gevonden. Het bestand is geblokkeerd.',
  mailbox_disabled: 'Deze mailbox staat uit en kan geen berichten versturen.',
  idempotency_conflict: 'Dit bericht kon niet worden verstuurd. Probeer het opnieuw.',
  link_invalid: 'Deze link is verlopen of al gebruikt.',
  system_mail_unavailable: 'Echoo kan geen e-mail versturen: er is geen systeemmailbox of SMTP-relay ingesteld.',
  oauth_provider_unavailable: 'Deze aanbieder is niet ingesteld.',
  email_in_use: 'Een van deze e-mailadressen hoort al bij een ander contact. Voeg de contacten samen als het dezelfde persoon is.',
  domain_in_use: 'Een van deze domeinen hoort al bij een andere organisatie.',
  gone: 'Deze export is al gedownload of verlopen. Vraag een nieuwe aan.',
  not_ready: 'De export is nog niet klaar.',
  export_failed: 'De export is mislukt. Probeer het opnieuw.',
  rules_changed: 'De regels zijn intussen door iemand anders gewijzigd. De lijst is vernieuwd.',
  in_use: 'Dit wordt nog gebruikt door een SLA-beleid.',
  action_failed: 'Een actie is mislukt.',
  role_in_use: 'Deze rol is nog in gebruik door gebruikers of als standaardrol voor SSO. Wijs eerst een andere rol toe.',
  sso_required: 'Inloggen met een wachtwoord staat uit. Gebruik single sign-on.',
  mfa_state_conflict: 'De status van tweestapsverificatie is intussen gewijzigd. Vernieuw de pagina.',
  campaign_state: 'De status van deze campagne is intussen gewijzigd. De pagina is vernieuwd.',
  test_send_failed: 'De testmail kon niet worden verstuurd.',
  sending: 'Er wordt nog mail uit dit gesprek verstuurd. Probeer het over een minuut opnieuw.',
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return messages[err.code] ?? messages.internal ?? ''
  return messages.internal ?? ''
}

const hostMessages = {
  invalid: 'Vul een hostnaam of IP-adres in, zonder https:// of pad.',
  internal_host: 'Deze server verwijst naar een intern adres. Alleen de eigenaar kan interne mailservers toestaan.',
}

const passwordMessages = { required: 'Vul een wachtwoord in.', invalid: 'Dit wachtwoord is ongeldig (maximaal 1024 tekens).' }

const attributeFieldMessages: Record<string, string> = {
  invalid: 'Deze waarde past niet bij het type veld.',
  unknown_attribute: 'Dit veld bestaat niet meer.',
  too_many: 'Er zijn te veel velden ingevuld.',
}

const fieldMessages: Record<string, Record<string, string>> = {
  emails: {
    invalid: 'Een van de e-mailadressen is ongeldig.',
    invalid_count: 'Vul 1 tot 10 e-mailadressen in.',
    duplicate: 'Een e-mailadres staat er dubbel in.',
    multiple_primary: 'Kies één primair e-mailadres.',
  },
  phone: { invalid: 'Gebruik maximaal 50 tekens, zonder regeleinden.' },
  organization_id: { invalid: 'Deze organisatie bestaat niet (meer).' },
  source_id: { invalid: 'Kies een ander contact.', same_contact: 'Kies een ander contact dan dit contact.' },
  confirmation: { mismatch: 'De invoer komt niet overeen met het e-mailadres van dit contact.' },
  domains: {
    invalid: 'Een van de domeinen is ongeldig.',
    free_mail: 'Gratis mailproviders zoals gmail.com kunnen geen organisatie herkennen.',
    duplicate: 'Een domein staat er dubbel in.',
    too_many: 'Gebruik maximaal 20 domeinen.',
  },
  key: { invalid: 'Gebruik kleine letters, cijfers en _ (maximaal 40 tekens, beginnend met een letter).', taken: 'Er bestaat al een veld met deze sleutel.' },
  label: { invalid: 'Vul een naam in (maximaal 60 tekens).' },
  options: { invalid: 'Vul 1 tot 50 keuzes in, elk maximaal 60 tekens.' },
  entity: { invalid: 'Kies waar het veld bij hoort.', limit_reached: 'Je kunt maximaal 50 velden per soort maken.' },
  type: { invalid: 'Kies een type.', immutable: 'Het type van een bestaand veld kun je niet wijzigen.' },
  filter: { invalid: 'Het filter is ongeldig.', too_large: 'Het filter is te groot.' },
  file: {
    required: 'Kies een bestand.',
    too_large: 'Het bestand is groter dan 20 MB.',
    too_many_rows: 'Het bestand heeft meer dan 50.000 rijen.',
    not_utf8: 'Het bestand is geen UTF-8. Sla het in Excel op als CSV UTF-8.',
    empty: 'Het bestand heeft geen kopregel met minstens één rij eronder.',
    invalid_csv: 'Het bestand is geen geldig CSV-bestand.',
  },
  body: { invalid: 'Schrijf een notitie van maximaal 10.000 tekens.' },

  url: {
    invalid: 'Vul een geldig webadres in, zonder gebruikersnaam of wachtwoord.',
    https_required: 'Gebruik een https-adres. Alleen de eigenaar kan http toestaan.',
    internal_host: 'Dit adres verwijst naar een intern netwerk en is niet toegestaan.',
    port_not_allowed: 'Gebruik poort 443 of 8443.',
  },
  events: { invalid: 'Kies minimaal één gebeurtenis.' },
  scope: { invalid: 'Kies wat het token mag doen.' },
  expires_at: { invalid: 'Kies een vervaldatum in de toekomst, maximaal vijf jaar vooruit.' },
  allow_http: { owner_only: 'Alleen de eigenaar kan http toestaan.' },
  include_content: { needs_full_access: 'Alleen een beheerder met toegang tot alle mailboxen kan berichtinhoud meesturen.' },
  email: { invalid: 'Vul een geldig e-mailadres in.', taken: 'Er bestaat al een gebruiker met dit e-mailadres.' },
  name: { invalid: 'Vul een naam in (maximaal 200 tekens).', taken: 'Deze naam is al in gebruik.', required: 'Vul een naam in.' },
  new_password: {
    too_short: 'Gebruik minimaal 12 tekens.',
    too_long: 'Gebruik maximaal 128 tekens.',
    same_as_current: 'Kies een ander wachtwoord dan je huidige.',
  },
  current_password: { incorrect: 'Dit wachtwoord klopt niet.' },
  password: { incorrect: 'Dit wachtwoord klopt niet.', too_short: 'Gebruik minimaal 12 tekens.', too_long: 'Gebruik maximaal 128 tekens.' },
  team_ids: { invalid: 'De teamkeuze is ongeldig.', unknown_team: 'Een van de gekozen teams bestaat niet meer.' },
  deactivated: { invited: 'Deze gebruiker heeft de uitnodiging nog niet geaccepteerd. Trek de uitnodiging in.' },
  role: { invalid: 'Kies een rol.', ambiguous: 'Kies een rol of een eigen rol, niet allebei.' },
  custom_role_id: { invalid: 'Kies een rol.', unknown: 'Deze rol bestaat niet meer.' },
  permissions: {
    unknown: 'Een van de rechten bestaat niet.',
    required: 'Kies minstens één recht.',
  },
  issuer_url: {
    invalid: 'Vul een https-adres in, zonder wachtwoord, vraagteken of #. Http mag alleen voor een intern adres.',
    required: 'Vul de issuer-URL in.',
    unreachable: 'Echoo kan de discovery-gegevens van deze issuer niet ophalen. Controleer het adres.',
    insecure: 'Deze issuer verwijst naar onbeveiligde adressen (http).',
  },
  client_id: { invalid: 'Gebruik maximaal 500 tekens.', required: 'Vul het client-ID in.' },
  client_secret: { invalid: 'Gebruik maximaal 2000 tekens.', required: 'Vul het client secret in.' },
  button_label: { invalid: 'Vul een tekst in van maximaal 40 tekens.' },
  allowed_domains: {
    invalid: 'Een domein is ongeldig. Gebruik bijvoorbeeld voorbeeld.nl.',
    too_many: 'Gebruik maximaal 50 domeinen.',
    required_for_provisioning: 'Vul minstens één domein in om nieuwe gebruikers automatisch aan te maken.',
  },
  default_role: { invalid: 'Kies een rol.', ambiguous: 'Kies een rol of een eigen rol, niet allebei.' },
  default_custom_role_id: {
    invalid: 'Kies een rol.',
    unknown: 'Deze rol bestaat niet meer.',
    privileged: 'Deze rol geeft beheerrechten en kan niet de standaardrol voor nieuwe gebruikers zijn.',
  },
  required: { needs_enabled: 'Zet SSO eerst aan.' },
  user_ids: { unknown_user: 'Een van de gekozen gebruikers bestaat niet meer.' },
  email_address: { invalid: 'Vul een geldig e-mailadres in.', taken: 'Er bestaat al een mailbox met dit e-mailadres.' },
  display_name: { invalid: 'Gebruik maximaal 200 tekens, zonder regeleinden.' },
  imap_host: hostMessages,
  smtp_host: hostMessages,
  imap_port: { invalid: 'Vul een poort tussen 1 en 65535 in.' },
  smtp_port: { invalid: 'Vul een poort tussen 1 en 65535 in.' },
  imap_tls: { invalid: 'Kies een beveiligingsmethode.' },
  smtp_tls: { invalid: 'Kies een beveiligingsmethode.' },
  imap_username: { invalid: 'Vul een gebruikersnaam in.' },
  smtp_username: { invalid: 'Gebruik een geldige gebruikersnaam.' },
  imap_password: passwordMessages,
  smtp_password: passwordMessages,
  sent_folder: { invalid: 'Gebruik een mapnaam van maximaal 200 tekens, zonder regeleinden.' },
  send_delay_seconds: { invalid: 'Kies een aantal seconden van 0 tot en met 30.' },
  access: { invalid: 'De toegang bevat een ongeldige waarde.', unknown_team: 'Een van de gekozen teams bestaat niet meer.' },
  id: { unknown: 'Deze mailbox bestaat niet meer.' },
  color_token: { invalid: 'Kies een kleur uit de lijst.' },
  description: { invalid: 'Gebruik maximaal 200 tekens, zonder regeleinden.' },
  snoozed_until: { invalid: 'Kies een moment in de toekomst.' },
  to: { invalid: 'Een van de ontvangers is geen geldig e-mailadres.', required: 'Vul minstens één ontvanger in.', too_many: 'Gebruik maximaal 50 ontvangers.' },
  cc: { invalid: 'Een van de Cc-ontvangers is geen geldig e-mailadres.', too_many: 'Gebruik maximaal 50 ontvangers.' },
  bcc: { invalid: 'Een van de Bcc-ontvangers is geen geldig e-mailadres.', too_many: 'Gebruik maximaal 50 ontvangers.' },
  subject: { invalid: 'Gebruik een onderwerp van maximaal 300 tekens, zonder regeleinden.', required: 'Vul een onderwerp in.' },
  html: { empty: 'Schrijf eerst een bericht.', too_large: 'Dit bericht is te groot.' },
  attachment_ids: { unknown: 'Een bijlage bestaat niet meer. Voeg hem opnieuw toe.', too_many: 'Voeg maximaal 20 bijlagen toe.', too_large: 'De bijlagen zijn samen groter dan 50 MB.' },
  mailbox_id: {
    invalid: 'Kies een mailbox.',
    required: 'Kies een mailbox.',
    unknown: 'Deze mailbox bestaat niet of je mag er niet vanuit versturen.',
    disabled: 'Deze mailbox staat uit.',
  },
  pattern: {
    required: 'Vul een e-mailadres of domein in.',
    invalid: 'Vul een e-mailadres (naam@voorbeeld.nl) of een domein (voorbeeld.nl) in.',
  },
  shortcode: { invalid: 'Gebruik kleine letters, cijfers, - en _ (maximaal 32 tekens).' },
  template_scope: { invalid: 'Kies voor wie dit standaardantwoord is.', unknown: 'Het gekozen team of de mailbox bestaat niet meer.' },
  team_id: { required: 'Kies een team.' },
  body_html: { required: 'Schrijf de tekst van het standaardantwoord.', too_large: 'Deze tekst is te groot.' },
  footer_text: { invalid: 'Gebruik maximaal 300 tekens, zonder regeleinden.' },
  trigger: { invalid: 'Kies wanneer de regel start.' },
  idle_hours: { invalid: 'Vul een aantal uren in van 1 tot 720.', not_allowed: 'Alleen nodig als de klant niet reageert.' },
  conditions: { invalid: 'De voorwaarden zijn ongeldig.', too_many: 'Gebruik maximaal 20 voorwaarden.' },
  actions: { invalid: 'De acties zijn ongeldig.', required: 'Voeg minstens één actie toe.', too_many: 'Gebruik maximaal 10 acties.' },
  ids: { invalid: 'De volgorde is ongeldig.' },
  weekly: { invalid: 'De openingstijden zijn ongeldig. Gebruik tijden als 09:00.', empty: 'Vul minstens één werktijd in.' },
  timezone: { invalid: 'Kies een bestaande tijdzone.' },
  holidays: { invalid: 'Een feestdag is geen geldige datum.', too_many: 'Gebruik maximaal 200 feestdagen.' },
  is_default: { one_default_required: 'Kies een ander schema als standaard om dit uit te zetten.', default_cannot_be_deleted: 'Het standaardschema kun je niet verwijderen.' },
  first_response_minutes: { invalid: 'Vul een aantal minuten in van 1 tot 525.600.', target_required: 'Vul minstens één doel in.' },
  resolution_minutes: { invalid: 'Vul een aantal minuten in van 1 tot 525.600.' },
  at_risk_percent: { invalid: 'Kies een percentage van 1 tot 99.' },
  business_hours_id: { invalid: 'Dit schema bestaat niet (meer).', unknown: 'Dit schema bestaat niet (meer).' },
  default_sla_policy_id: { invalid: 'Dit beleid bestaat niet (meer).', unknown: 'Dit beleid bestaat niet (meer).' },
  auto_assign_mode: { invalid: 'Kies een manier van toewijzen.' },
  max_open: { invalid: 'Vul een aantal in van 1 tot 10.000, of laat het leeg.', required: 'Vul een aantal in.' },
  auto_resolve_days: { invalid: 'Vul een aantal dagen in van 0 tot 365.' },
  availability: { invalid: 'Kies online, bezet of offline.' },
  conversation_ids: { count: 'Kies 1 tot 500 gesprekken.', invalid: 'Een van de gesprekken is ongeldig.' },
  segment_id: { invalid: 'Kies een segment.', unknown: 'Dit segment bestaat niet of is niet van jou en niet gedeeld.', required: 'Kies een segment.' },
  rate_per_minute: { invalid: 'Vul een aantal in binnen de toegestane snelheid.' },
  scheduled_at: { invalid: 'Kies een moment in de toekomst, maximaal een jaar vooruit.' },
}

export function fieldError(err: unknown, field: string): string | undefined {
  if (!(err instanceof ApiError)) return undefined
  const code = err.fields[field]
  if (!code) return undefined
  if (code === 'managed_by_oauth') return 'Dit volgt uit het gekoppelde account. Verbind het account opnieuw om het te wijzigen.'
  const table = field.startsWith('custom_attributes.') ? attributeFieldMessages : fieldMessages[field]
  return table?.[code] ?? 'Ongeldige waarde.'
}

const probeReasons: Record<string, string> = {
  connect_failed: 'geen verbinding — controleer server en poort',
  tls_failed: 'beveiligde verbinding mislukt — controleer de beveiliging en het certificaat',
  auth_failed: 'inloggen mislukt — controleer gebruikersnaam en wachtwoord',
  no_inbox: 'verbonden, maar de map INBOX ontbreekt',
  oauth_reauth_required: 'de koppeling is verlopen of ingetrokken — verbind het account opnieuw',
}

export function probeText(protocol: 'IMAP' | 'SMTP', result: ProbeResult): string {
  if (result.ok) return `${protocol}: verbonden`
  const reason = probeReasons[(result.code ?? '').replace(/^(imap|smtp)_/, '')]
  return `${protocol}: ${reason ?? 'verbinding mislukt'}`
}
