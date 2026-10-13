import { useState } from "react";
import type { CloudAgentChatMessage } from "./canvas-cloud-agent-chat-ui";

/** Per-message edit/delete for the canvas sidebar; changes sync to the intelligence directory (tombstones for deletes). */
export function AgentMessageEditControls({ item, onEdit, onDelete }: { item: CloudAgentChatMessage; onEdit?: (messageId: string, text: string) => void; onDelete?: (messageId: string) => void }) {
    const [editing, setEditing] = useState(false);
    const [draft, setDraft] = useState(item.text);
    const [confirmDelete, setConfirmDelete] = useState(false);
    if (editing) {
        return (
            <div className="agent-message-edit mt-1 flex flex-col gap-1 px-3" data-agent-message-edit={item.id}>
                <textarea className="w-full rounded border bg-transparent p-1 text-xs" rows={3} value={draft} aria-label="编辑消息" onChange={(event) => setDraft(event.target.value)} />
                <div className="flex justify-end gap-2 text-xs">
                    <button type="button" onClick={() => setEditing(false)}>
                        取消
                    </button>
                    <button
                        type="button"
                        disabled={!draft.trim()}
                        onClick={() => {
                            onEdit?.(item.id, draft);
                            setEditing(false);
                        }}
                    >
                        保存
                    </button>
                </div>
            </div>
        );
    }
    return (
        <div className="agent-message-actions flex justify-end gap-2 px-3 text-xs opacity-60 hover:opacity-100" data-agent-message-actions={item.id}>
            {onEdit ? (
                <button
                    type="button"
                    onClick={() => {
                        setDraft(item.text);
                        setEditing(true);
                    }}
                >
                    编辑
                </button>
            ) : null}
            {onDelete ? (
                <button
                    type="button"
                    onClick={() => {
                        if (!confirmDelete) return setConfirmDelete(true);
                        setConfirmDelete(false);
                        onDelete(item.id);
                    }}
                    onBlur={() => setConfirmDelete(false)}
                >
                    {confirmDelete ? "确认删除" : "删除"}
                </button>
            ) : null}
        </div>
    );
}
