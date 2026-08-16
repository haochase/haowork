import type { SkillDefinition } from "../api/types";

export function SkillActivity({ skills }: { skills: SkillDefinition[] }) {
  return <section className="governance-panel" aria-labelledby="skill-activity-title"><h2 id="skill-activity-title">Skill Activity</h2><ul>{skills.map((skill) => <li key={`${skill.name}-${skill.version}`}><strong>{skill.name}</strong><span>{skill.version} · {skill.risk}</span></li>)}</ul></section>;
}
