import { useState, type FormEvent } from 'react';

type Props = {
    categories: string[];
    customCategories: string[];
    usage: Record<string, number>;
    busy: boolean;
    onAdd: (name: string) => Promise<boolean>;
    onRename: (name: string, newName: string) => Promise<boolean>;
    onRemove: (name: string, replacement: string) => Promise<boolean>;
};

export function CategorySettings({ categories, customCategories, usage, busy, onAdd, onRename, onRemove }: Props) {
    const [newName, setNewName] = useState('');
    const [editing, setEditing] = useState('');
    const [editName, setEditName] = useState('');
    const [removing, setRemoving] = useState('');
    const [replacement, setReplacement] = useState('Other');

    async function add(event: FormEvent) {
        event.preventDefault();
        if (await onAdd(newName.trim())) setNewName('');
    }
    async function rename(event: FormEvent, name: string) {
        event.preventDefault();
        if (await onRename(name, editName.trim())) setEditing('');
    }
    async function remove(name: string) {
        if (await onRemove(name, usage[name] ? replacement : '')) setRemoving('');
    }

    return <section className="settings-panel category-settings">
        <h3>Your spending labels</h3>
        <p>Create labels that fit your expenses. Choose one when reviewing an alert; your monthly spending will use that label.</p>
        <form className="label-add" onSubmit={add}>
            <label htmlFor="new-category">New label</label>
            <div><input id="new-category" value={newName} onChange={event => setNewName(event.target.value)} maxLength={40} placeholder="e.g. Subscriptions" required disabled={busy}/><button type="submit" disabled={busy || !newName.trim()}>Add label</button></div>
        </form>
        <div className="label-list-heading"><strong>Your labels</strong><span>{customCategories.length}</span></div>
        {customCategories.length ? <ul className="custom-label-list">{customCategories.map(name => <li key={name}>
            {editing === name ? <form className="label-edit" onSubmit={event => rename(event, name)}><input aria-label={`New name for ${name}`} value={editName} onChange={event => setEditName(event.target.value)} maxLength={40} required disabled={busy}/><button type="submit" disabled={busy || !editName.trim()}>Save</button><button type="button" disabled={busy} onClick={() => setEditing('')}>Cancel</button></form> : <div className="label-row"><span><strong>{name}</strong><small>{usage[name] || 0} expense{usage[name] === 1 ? '' : 's'}</small></span><div><button className="text-link" disabled={busy} onClick={() => { setEditing(name); setEditName(name); setRemoving(''); }}>Rename</button><button className="text-link" disabled={busy} onClick={() => { setRemoving(name); setReplacement('Other'); setEditing(''); }}>Remove</button></div></div>}
            {removing === name && <div className="label-remove"><p>Remove “{name}”?{usage[name] ? ` Move its ${usage[name]} expenses to another label.` : ''}</p>{!!usage[name] && <label>Move expenses to<select value={replacement} onChange={event => setReplacement(event.target.value)} disabled={busy}>{categories.filter(category => category !== name).map(category => <option key={category}>{category}</option>)}</select></label>}<div><button type="button" disabled={busy} onClick={() => remove(name)}>Remove label</button><button type="button" disabled={busy} onClick={() => setRemoving('')}>Cancel</button></div></div>}
        </li>)}</ul> : <p className="small muted">No personal labels yet.</p>}
        <p className="small muted">The built-in labels remain available. Gmail’s Review, Accepted, and Ignored labels are managed separately.</p>
    </section>;
}
