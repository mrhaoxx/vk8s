// 等宽标签组：节点池约束、taints 等。
export function Chips({ items }: { items: string[] }) {
  return (
    <div className="chips">
      {items.map((s) => (
        <code key={s} className="chip">
          {s}
        </code>
      ))}
    </div>
  );
}
