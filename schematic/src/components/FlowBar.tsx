// The bar over the canvas while a walk-through is open: the story's name, which step, its caption,
// and back, play and next.
import { Icon } from "../icons.tsx";
import type { Flow } from "../types.ts";

interface Props {
  flow: Flow;
  step: number;
  playing: boolean;
  hasNext: boolean;
  hasBack: boolean;
  onNext: () => void;
  onBack: () => void;
  onPlay: () => void;
  onClose: () => void;
}

export function FlowBar({
  flow,
  step,
  playing,
  hasNext,
  hasBack,
  onNext,
  onBack,
  onPlay,
  onClose,
}: Props) {
  const s = flow.steps[step];
  return (
    <section className="flowbar" aria-label={`Walk-through: ${flow.name}`}>
      <div className="flowbar-top">
        <span className="flowbar-name">{flow.name}</span>
        <span className="flowbar-count">
          Step {step + 1} of {flow.steps.length}
        </span>
        <span className="flowbar-dots" aria-hidden="true">
          {flow.steps.map((x, i) => (
            <span key={x.caption} className={i === step ? "on" : i < step ? "done" : ""} />
          ))}
        </span>
        <button
          type="button"
          className="flowbar-close"
          onClick={onClose}
          title="Close the walk-through (Esc)"
          aria-label="Close the walk-through"
        >
          <Icon name="close" size={13} />
        </button>
      </div>
      <p className="flowbar-caption" key={`${flow.id}:${step}`}>
        {s.caption}
      </p>
      <div className="flowbar-controls">
        <button
          type="button"
          className="flowbar-btn"
          onClick={onBack}
          disabled={!hasBack}
          title="Back (←)"
        >
          <Icon name="back" size={13} />
          Back
        </button>
        <button
          type="button"
          className="flowbar-btn flowbar-play"
          onClick={onPlay}
          title="Play (space)"
        >
          <Icon name={playing ? "pause" : "play"} size={13} />
          {playing ? "Pause" : "Play"}
        </button>
        <button
          type="button"
          className="flowbar-btn"
          onClick={onNext}
          disabled={!hasNext}
          title="Next (→)"
        >
          Next
          <Icon name="chevron" size={13} />
        </button>
      </div>
    </section>
  );
}
