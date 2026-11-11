/** The auto-repair doctor's state (scripts/tfg-doctor.py → ~/.founderos/doctor/state.json), as /pages/doctor returns it. */
export type RepairCheck = {
	label?: string;
	status?: 'ok' | 'failing' | 'repairing' | 'queued' | 'needs_owner' | 'unknown' | (string & {});
	detail?: string;
	since?: string;
	checkedAt?: string;
	note?: string | null;
	ownerAction?: string | null;
	ai?: { status?: string; summary?: string; startedAt?: string; finishedAt?: string } | null;
};
export type RepairState = {
	lastPass?: string;
	checks?: Record<string, RepairCheck>;
	actions?: { at: string; check: string; text: string }[];
	holds?: string[];
};

export type RepairRow = { id: string; label: string; status: string; detail: string; action: string | null; tone: 'ok' | 'warn' | 'err' };

const ORDER: Record<string, number> = { needs_owner: 0, failing: 1, unknown: 2, repairing: 3, queued: 4, ok: 5 };

/** Rows worst first; what needs the owner on top. */
export function repairRows(s: RepairState | null | undefined): RepairRow[] {
	const checks = s?.checks ?? {};
	return Object.entries(checks)
		.map(([id, c]) => {
			const status = c.status ?? 'unknown';
			const tone: RepairRow['tone'] = status === 'ok' ? 'ok' : status === 'needs_owner' || status === 'failing' ? 'err' : 'warn';
			const action = status === 'needs_owner' ? (c.ownerAction ?? null) : status === 'ok' ? null : (c.note ?? null);
			return { id, label: c.label ?? id, status, detail: c.detail ?? '', action, tone };
		})
		.sort((a, b) => (ORDER[a.status] ?? 5) - (ORDER[b.status] ?? 5) || a.label.localeCompare(b.label));
}

export function repairHeadline(s: RepairState | null | undefined): { text: string; tone: 'ok' | 'warn' | 'err' } {
	if (!s?.lastPass) return { text: 'auto-repair has not run yet', tone: 'warn' };
	const rows = repairRows(s);
	const owner = rows.filter((r) => r.status === 'needs_owner').length;
	const fixing = rows.filter((r) => r.status !== 'ok' && r.status !== 'needs_owner').length;
	if (owner) return { text: `${owner} need${owner === 1 ? 's' : ''} you · ${fixing} being fixed`, tone: 'err' };
	if (fixing) return { text: `${fixing} being fixed`, tone: 'warn' };
	return { text: `all ${rows.length} checks healthy`, tone: 'ok' };
}

const LABEL: Record<string, string> = { needs_owner: 'needs you', failing: 'failing', repairing: 'AI repairing', queued: 'queued', unknown: "couldn't check", ok: 'ok' };
export const statusLabel = (s: string) => LABEL[s] ?? s;
