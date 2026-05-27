import {
  Button,
  Field,
  Input,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { Add20Regular, Delete20Regular } from '@fluentui/react-icons';

const useStyles = makeStyles({
  labelRow: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr auto',
    gap: tokens.spacingHorizontalS,
    alignItems: 'end',
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

export interface LabelPair {
  key: string;
  value: string;
}

export interface LabelEditorProps {
  labels: LabelPair[];
  onChange: (l: LabelPair[]) => void;
}

export function LabelEditor({ labels, onChange }: LabelEditorProps) {
  const styles = useStyles();

  const add = () => {
    if (labels.length >= 50) return;
    onChange([...labels, { key: '', value: '' }]);
  };

  const remove = (i: number) => onChange(labels.filter((_, idx) => idx !== i));

  const update = (i: number, patch: Partial<LabelPair>) =>
    onChange(labels.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalS }}>
      <div className={styles.sectionTitle}>
        Labels <span className={styles.infoText}>(max 50)</span>
      </div>
      {labels.map((l, i) => (
        <div key={i} className={styles.labelRow}>
          <Field label={i === 0 ? 'Key' : undefined}>
            <Input
              value={l.key}
              onChange={(_, d) => update(i, { key: d.value })}
              placeholder="team"
              size="small"
            />
          </Field>
          <Field label={i === 0 ? 'Value' : undefined}>
            <Input
              value={l.value}
              onChange={(_, d) => update(i, { value: d.value })}
              placeholder="ml"
              size="small"
            />
          </Field>
          <Button
            appearance="subtle"
            icon={<Delete20Regular />}
            onClick={() => remove(i)}
            aria-label="Remove label"
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
        disabled={labels.length >= 50}
      >
        Add label
      </Button>
    </div>
  );
}
