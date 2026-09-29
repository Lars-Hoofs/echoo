package audit

const (
	UserInvited            = "user.invited"
	InvitationResent       = "user.invitation_resent"
	InvitationRevoked      = "user.invitation_revoked"
	InvitationAccepted     = "user.invitation_accepted"
	PasswordResetRequested = "auth.password_reset_requested"
	PasswordResetCompleted = "auth.password_reset_completed"
	MailboxOAuthConnected  = "mailbox.oauth_connected"
	MailboxOAuthFailed     = "mailbox.oauth_failed"
)
