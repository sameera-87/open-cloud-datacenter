import {
  Button,
  Dropdown,
  Field,
  Input,
  makeStyles,
  Option,
  tokens,
} from '@fluentui/react-components';
import { Add20Regular, Delete20Regular } from '@fluentui/react-icons';

export interface NodePoolTaint {
  key: string;
  value?: string;
  effect: 'NoSchedule' | 'PreferNoSchedule' | 'NoExecute';
}

const TAINT_EFFECTS = ['NoSchedule', 'PreferNoSchedule', 'NoExecute'] as const;
type TaintEffect = (typeof TAINT_EFFECTS)[number];

const useStyles = makeStyles({
  taintRow: {
    display: 'grid',
    // minmax(0, ...) lets cells shrink below their content's intrinsic width
    // — without this, the Effect dropdown's "PreferNoSchedule" text expands
    // its cell past the 140px allowance and pushes the delete button off
    // the right edge of the worker-pool container.
    gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr) minmax(0, 140px) auto',
    gap: tokens.spacingHorizontalS,
    alignItems: 'end',
  },
  cellFill: {
    width: '100%',
    minWidth: 0,
  },
  sectionTitle: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    marginBottom: tokens.spacingVerticalXS,
  },
  infoText: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
  },
  addRowBtn: {
    alignSelf: 'flex-start',
  },
});

export interface TaintEditorProps {
  taints: NodePoolTaint[];
  onChange: (t: NodePoolTaint[]) => void;
}

export function TaintEditor({ taints, onChange }: TaintEditorProps) {
  const styles = useStyles();

  const add = () => {
    if (taints.length >= 10) return;
    onChange([...taints, { key: '', effect: 'NoSchedule' }]);
  };

  const remove = (i: number) => onChange(taints.filter((_, idx) => idx !== i));

  const update = (i: number, patch: Partial<NodePoolTaint>) =>
    onChange(taints.map((t, idx) => (idx === i ? { ...t, ...patch } : t)));

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalS }}>
      <div className={styles.sectionTitle}>
        Taints <span className={styles.infoText}>(max 10)</span>
      </div>
      {taints.map((t, i) => (
        <div key={i} className={styles.taintRow}>
          <Field label={i === 0 ? 'Key' : undefined}>
            <Input
              value={t.key}
              onChange={(_, d) => update(i, { key: d.value })}
              placeholder="nvidia.com/gpu"
              size="small"
              className={styles.cellFill}
            />
          </Field>
          <Field label={i === 0 ? 'Value (optional)' : undefined}>
            <Input
              value={t.value ?? ''}
              onChange={(_, d) => update(i, { value: d.value || undefined })}
              placeholder="present"
              size="small"
              className={styles.cellFill}
            />
          </Field>
          <Field label={i === 0 ? 'Effect' : undefined}>
            <Dropdown
              value={t.effect}
              selectedOptions={[t.effect]}
              onOptionSelect={(_, d) =>
                update(i, { effect: (d.optionValue ?? 'NoSchedule') as TaintEffect })
              }
              size="small"
              className={styles.cellFill}
            >
              {TAINT_EFFECTS.map((e) => (
                <Option key={e} value={e}>
                  {e}
                </Option>
              ))}
            </Dropdown>
          </Field>
          <Button
            appearance="subtle"
            icon={<Delete20Regular />}
            onClick={() => remove(i)}
            aria-label="Remove taint"
            size="small"
          />
        </div>
      ))}
      <Button
        className={styles.addRowBtn}
        appearance="secondary"
        icon={<Add20Regular />}
        size="small"
        onClick={add}
        disabled={taints.length >= 10}
      >
        Add taint
      </Button>
    </div>
  );
}
