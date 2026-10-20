import { useEffect, useRef, useState } from 'react';

type Option = { value: string; label: string };

export function FilterSelect({ id, label, value, options, onChange, disabled = false }: {
    id: string;
    label: string;
    value: string;
    options: Option[];
    onChange: (value: string) => void;
    disabled?: boolean;
}) {
    const [open, setOpen] = useState(false);
    const selected = Math.max(0, options.findIndex(option => option.value === value));
    const [active, setActive] = useState(selected);
    const root = useRef<HTMLDivElement>(null);
    const search = useRef({ text: '', time: 0 });

    useEffect(() => { if (disabled) setOpen(false); }, [disabled]);

    useEffect(() => {
        if (!open) return;
        function outside(event: PointerEvent) {
            if (!root.current?.contains(event.target as Node)) setOpen(false);
        }
        document.addEventListener('pointerdown', outside);
        return () => document.removeEventListener('pointerdown', outside);
    }, [open]);

    useEffect(() => {
        if (open) root.current?.querySelector(`[data-option="${active}"]`)?.scrollIntoView({ block: 'nearest' });
    }, [open, active]);

    const expanded = open && !disabled;
    function choose(index: number) {
        if (options[index]) onChange(options[index].value);
        setOpen(false);
    }

    return <div className="filter-field" ref={root} onBlur={event => {
        if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false);
    }}>
        <label id={`${id}-label`} htmlFor={id}>{label}</label>
        <div className="filter-select">
            <button id={id} type="button" role="combobox" className="filter-select-trigger"
                disabled={disabled} aria-expanded={expanded} aria-haspopup="listbox"
                aria-controls={`${id}-options`} aria-labelledby={`${id}-label ${id}-value`}
                aria-activedescendant={expanded ? `${id}-option-${active}` : undefined}
                onClick={() => { setActive(selected); setOpen(!expanded); }}
                onKeyDown={event => {
                    if (event.key === 'Escape') { event.preventDefault(); setOpen(false); return; }
                    if (event.key === 'Tab') { setOpen(false); return; }
                    if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault();
                        if (expanded) choose(active);
                        else { setActive(selected); setOpen(true); }
                        return;
                    }
                    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
                        event.preventDefault();
                        setOpen(true);
                        const start = expanded ? active : selected;
                        setActive(event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1 :
                            Math.max(0, Math.min(options.length - 1, start + (event.key === 'ArrowDown' ? 1 : -1))));
                        return;
                    }
                    if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
                        event.preventDefault();
                        const now = Date.now();
                        search.current = { text: (now - search.current.time < 600 ? search.current.text : '') + event.key.toLowerCase(), time: now };
                        const index = options.findIndex(option => option.label.toLowerCase().startsWith(search.current.text));
                        if (index !== -1) { setActive(index); setOpen(true); }
                    }
                }}>
                <span id={`${id}-value`}>{options[selected]?.label}</span>
                <svg aria-hidden="true" width="9" height="6" viewBox="0 0 9 6"><path d="M1 1l3.5 3.5L8 1" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>
            </button>
            {expanded && <div id={`${id}-options`} className="filter-select-menu" role="listbox" aria-labelledby={`${id}-label`}>
                {options.map((option, index) => <div key={option.value} id={`${id}-option-${index}`}
                    role="option" aria-selected={option.value === value} data-option={index}
                    className={`filter-select-option${active === index ? ' is-active' : ''}`}
                    onPointerMove={() => setActive(index)} onMouseDown={event => event.preventDefault()}
                    onClick={() => choose(index)}>
                    <span>{option.label}</span>
                    {option.value === value && <svg aria-hidden="true" width="11" height="9" viewBox="0 0 11 9"><path d="M1 4l3 3 6-6" fill="none" stroke="currentColor" strokeWidth="1.2" /></svg>}
                </div>)}
            </div>}
        </div>
    </div>;
}
