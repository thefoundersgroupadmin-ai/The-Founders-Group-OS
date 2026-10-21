// GET /api/founderos/pages/outbox (internal/founderos/pages/outbox).
export type OutboxStatus = 'pending' | 'sending' | 'sent' | 'rejected' | 'failed' | 'uncertain';

export type OutboxItem = {
	id: string;
	source: string;
	sourceRef: string | null;
	contactId: string | null;
	accountId: string | null;
	to: string;
	toName: string | null;
	company: string | null;
	subject: string;
	body: string;
	status: OutboxStatus;
	error: string | null;
	attempt: number;
	createdAt: string;
	decidedAt: string | null;
	sentAt: string | null;
};

export type OutboxBody = { pending: OutboxItem[]; recent: OutboxItem[] };

export type OutboxDecision = { ok: boolean; item?: OutboxItem; detail?: string; warning?: string; guarded?: boolean };
