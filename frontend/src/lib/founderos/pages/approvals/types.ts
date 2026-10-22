// GET /api/founderos/pages/approvals (connectors/paperclip/approvals.go).
export type ApprovalOption = {
	id: string;
	label: string;
	description: string | null;
	freeText: boolean;
};

export type ApprovalQuestion = {
	id: string;
	prompt: string;
	helpText: string | null;
	selectionMode: 'single' | 'multi' | string;
	required: boolean;
	allowOther: boolean;
	options: ApprovalOption[];
};

export type SuggestedTask = {
	clientKey: string;
	title: string;
	description: string;
};
export type VerdictItem = {
	id: string;
	label: string;
	description: string | null;
	preview: string;
	/** The verdict already recorded on the board; that item is not offered again. */
	resolved: VerdictValue | null;
	resolvedReason: string | null;
};
export type VerdictValue = 'approve' | 'reject' | 'defer';

export type ApprovalItem = {
	id: string;
	issueId: string;
	issueIdentifier: string;
	issueTitle: string;
	kind: 'request_confirmation' | 'ask_user_questions' | 'suggest_tasks' | 'request_checkbox_confirmation' | 'request_item_verdicts' | string;
	title: string;
	prompt: string;
	details: string;
	acceptLabel: string;
	rejectLabel: string;
	rejectRequiresReason: boolean;
	questions: ApprovalQuestion[];
	checkboxes: ApprovalOption[];
	defaultSelected: string[];
	minSelected: number;
	maxSelected: number | null;
	tasks: SuggestedTask[];
	items: VerdictItem[];
	verdicts: VerdictValue[];
	requireReasonOn: VerdictValue[];
	createdAt: string;
};

export type ApprovalsBody = {
	items: ApprovalItem[];
	warnings: string[];
	outboxPending: number;
	boardError?: string;
};

export type TaskDocument = {
	key: string;
	title: string;
	body: string;
	updatedAt: string;
};
export type TaskComment = {
	id: string;
	body: string;
	authorType: string;
	authorAgentId: string | null;
	createdAt: string;
};
export type TaskContext = { documents: TaskDocument[]; thread: TaskComment[] };
