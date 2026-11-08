// GET /api/founderos/pods and /pods/:slug (internal/founderos/api/page_pods.go).
export type PodStatus = 'setup' | 'dry_run' | 'live' | 'paused' | 'handed_over';
export type ApprovalPolicy = 'client_approves_each' | 'client_approves_sequences' | 'auto';

export type Pod = {
	slug: string;
	clientName: string;
	package: 'custom' | 'core' | 'full';
	reps: number;
	status: PodStatus;
	senderName: string;
	senderEmail: string;
	sendingDomain: string;
	approvalPolicy: ApprovalPolicy;
	firstEmailDailyCap: number;
	followUpDailyCap: number;
	timezone: string;
	sendWindow: [number, number];
	sendWeekends: boolean;
	dryRunInbox: string;
	crmProvider: string;
	calendarProvider: string;
	bookingUrl: string;
	pausedReason: string;
	portalClientId: string;
	wentLiveAt: string | null;
	missingToGoLive?: string[];
};

export type PodDoc = { key: string; body: string; version: number; approvedAt: string | null; updatedAt: string };
export type Mailbox = { configured: boolean; host: string; user: string; from: string; fromName: string; smtpHost: string; smtpPort: string; pass: string };
export type Activity = { at: string; kind: string; summary: string; ref: string };
export type AgentBinding = { projectId: string; agentId: string; researchRoutineId: string; outreachRoutineId: string; routinesActive: boolean };
export type ClientApproval = { kind: string; ref: string; title: string; status: 'pending' | 'approved' | 'changes_requested' | 'withdrawn' };
