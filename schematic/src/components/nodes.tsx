import { Handle, type NodeProps, Position } from "@xyflow/react";
import { memo } from "react";
import { useFocus } from "../focus.ts";
import type { CardModel, ClassModel, GroupModel, NodeModel } from "../graph.ts";
import { Icon, KindIcon } from "../icons.tsx";
import { kindColor } from "../kinds.ts";
import type { Kind } from "../types.ts";

export interface NodeData extends Record<string, unknown> {
  model: NodeModel;
  kind: Kind;
  onOpen?: (id: string) => void;
  onFold?: (id: string) => void;
}

// Edges are drawn from the layout's own points, so handles only anchor them; they stay invisible.
function Anchors() {
  return (
    <>
      <Handle type="target" position={Position.Left} isConnectable={false} className="anchor" />
      <Handle type="source" position={Position.Right} isConnectable={false} className="anchor" />
    </>
  );
}

function useDim(id: string) {
  const f = useFocus();
  const dim = f.nodes !== null && !f.nodes.has(id);
  const lit = f.nodes?.has(id) ?? false;
  return { dim, lit, selected: f.selectedNode === id };
}

// SettingLine draws the setting's name, on its first line, apart from its choices.
function SettingLine({ line, first }: { line: string; first: boolean }) {
  const colon = first ? line.indexOf(":") : -1;
  if (colon < 0) return <>{line}</>;
  return (
    <>
      <span className="setting-name">{line.slice(0, colon + 1)}</span>
      {line.slice(colon + 1)}
    </>
  );
}

export const CardNode = memo(function CardNode({ id, data }: NodeProps) {
  const { model, kind, onOpen, onFold } = data as NodeData;
  const m = model as CardModel;
  const { dim, lit, selected } = useDim(id);
  const ghost = m.variant === "ghost";
  const cls = [
    "card",
    ghost ? "ghost" : "",
    dim ? "dim" : "",
    lit ? "lit" : "",
    selected ? "selected" : "",
    m.footer ? "drillable" : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={cls} style={{ "--accent": kindColor(kind) } as React.CSSProperties}>
      <Anchors />
      <div className="card-head">
        <div className="card-icon">
          <KindIcon kind={kind} size={ghost ? 14 : 16} />
        </div>
        <div className="card-titles">
          <div className="card-eyebrow">{m.eyebrow}</div>
          <div className="card-name">{m.node.name}</div>
        </div>
      </div>
      {m.path && <div className="card-path">{m.path}</div>}
      {m.tech && <div className={m.path ? "card-tech tight" : "card-tech"}>{m.tech}</div>}
      {m.setting.length > 0 && (
        <div className="card-setting" title={m.node.setting}>
          {m.setting.map((l, i) => (
            <div key={l}>
              <SettingLine line={l} first={i === 0} />
            </div>
          ))}
        </div>
      )}
      {m.summary.length > 0 && (
        <div className="card-summary">
          {m.summary.map((l) => (
            <div key={l}>{l}</div>
          ))}
        </div>
      )}
      {m.members.length > 0 && (
        <div className="card-members">
          {m.members.map((l) => (
            <div key={l}>
              <span className="member-dot" />
              {l}
            </div>
          ))}
        </div>
      )}
      {m.footer && (
        <button
          type="button"
          className="card-footer nodrag"
          onClick={(e) => {
            e.stopPropagation();
            onOpen?.(id);
          }}
        >
          <span>{m.footer}</span>
          <Icon name="chevron" size={14} />
        </button>
      )}
      {m.fold && (
        <button
          type="button"
          className={m.fold.open ? "card-footer card-fold unfolded" : "card-footer card-fold"}
          onClick={(e) => {
            e.stopPropagation();
            onFold?.(id);
          }}
        >
          <span>
            {m.fold.open ? "Fold" : "Show"} {m.fold.count}{" "}
            {m.fold.count === 1 ? "adapter" : "adapters"}
          </span>
          <Icon name="chevron" size={14} />
        </button>
      )}
    </div>
  );
});

export const GroupNode = memo(function GroupNode({ id, data }: NodeProps) {
  const { model } = data as NodeData;
  const m = model as GroupModel;
  const f = useFocus();
  const selected = f.selectedNode === id;
  return (
    <div
      className={[
        "box",
        m.external ? "external" : "",
        selected ? "selected" : "",
        `accent-${m.accent}`,
      ].join(" ")}
      style={{ "--accent": kindColor(m.accent) } as React.CSSProperties}
    >
      <Anchors />
      <div className="box-head">
        <span className="box-icon">
          <KindIcon kind={m.accent} size={13} />
        </span>
        <span className="box-eyebrow">{m.eyebrow}</span>
        <span className="box-title">{m.title}</span>
        {m.path && <span className="box-path">{m.path}</span>}
      </div>
    </div>
  );
});

export const ClassNode = memo(function ClassNode({ id, data }: NodeProps) {
  const { model } = data as NodeData;
  const m = model as ClassModel;
  const t = m.type;
  const { dim, lit, selected } = useDim(id);
  const iface = t.kind === "interface";
  const cls = [
    "cls",
    iface ? "iface" : "struct",
    t.external ? "stub" : "",
    t.exported ? "" : "unexported",
    dim ? "dim" : "",
    lit ? "lit" : "",
    selected ? "selected" : "",
  ]
    .filter(Boolean)
    .join(" ");
  const pkgName = t.external ? t.package.split("/").pop() : undefined;
  return (
    <div className={cls}>
      <Anchors />
      <div className="cls-head">
        <span className="cls-name">
          {pkgName && <span className="cls-pkg">{pkgName}.</span>}
          {t.name}
        </span>
        <span className="cls-badge">{t.kind}</span>
      </div>
      {m.fields.length > 0 && (
        <div className="cls-section">
          {m.fields.map((f) => (
            <div
              key={f.full}
              className={["cls-line", f.exported ? "" : "muted", f.related ? "related" : ""].join(
                " ",
              )}
              title={f.full}
            >
              {f.name && <span className="cls-member">{f.name}</span>}
              <span className="cls-type">{f.rest}</span>
            </div>
          ))}
        </div>
      )}
      {m.methods.length > 0 && (
        <div className="cls-section">
          {m.methods.map((f) => (
            <div
              key={f.full}
              className={["cls-line", f.exported ? "" : "muted"].join(" ")}
              title={f.full}
            >
              <span className="cls-member method">{f.name}</span>
              <span className="cls-type">{f.rest}</span>
            </div>
          ))}
        </div>
      )}
      {m.hidden > 0 && <div className="cls-more">+ {m.hidden} unexported</div>}
    </div>
  );
});

export const nodeTypes = { card: CardNode, box: GroupNode, cls: ClassNode };
