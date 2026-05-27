import {
  Body1,
  Body2,
  Button,
  Subtitle2,
  makeStyles,
  shorthands,
  tokens,
} from '@fluentui/react-components';
import { useState } from 'react';
import {
  ArrowDownload20Regular,
  Copy20Regular,
  Dismiss20Regular,
  Eye20Regular,
  EyeOff20Regular,
  Key20Filled,
} from '@fluentui/react-icons';

/**
 * Bespoke "shown-once" secrets banner — used after VM create (SSH key
 * + console password), cluster create (kubeconfig), and service-account
 * create (token). dc-api intentionally never persists these; the banner
 * is the user's only chance to copy/save.
 *
 * Each `Secret` becomes a row with a label, the value rendered as a
 * monospace code block, and copy + (optional) download actions.
 */

export interface Secret {
  label: string;
  value: string;
  filename?: string; // if set, "Download" button writes a file with this name
  multiline?: boolean; // PEM keys etc. — use a tall block; passwords use a one-liner
}

const useStyles = makeStyles({
  root: {
    backgroundColor: tokens.colorPaletteYellowBackground1,
    color: tokens.colorPaletteDarkOrangeForeground2,
    ...shorthands.border('1px', 'solid', tokens.colorPaletteYellowBorder1),
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalL,
    display: 'grid',
    gridTemplateColumns: 'auto 1fr auto',
    gap: tokens.spacingHorizontalM,
    alignItems: 'start',
  },
  iconWrap: {
    width: '32px',
    height: '32px',
    borderRadius: tokens.borderRadiusCircular,
    backgroundColor: tokens.colorPaletteDarkOrangeBackground2,
    color: tokens.colorPaletteDarkOrangeForeground2,
    display: 'grid',
    placeItems: 'center',
    flexShrink: 0,
  },
  body: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalM, minWidth: 0 },
  desc: { color: tokens.colorNeutralForeground2 },
  secretRow: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalXS },
  secretLabel: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground2,
    fontWeight: tokens.fontWeightSemibold,
  },
  secretValueWrap: {
    position: 'relative',
    backgroundColor: tokens.colorNeutralBackground1,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalM,
    paddingRight: '120px',
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground1,
  },
  secretValueOneLine: {
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  },
  secretValueMulti: {
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-all',
    maxHeight: '160px',
    overflowY: 'auto',
  },
  actions: {
    position: 'absolute',
    top: tokens.spacingVerticalXS,
    right: tokens.spacingHorizontalXS,
    display: 'flex',
    gap: tokens.spacingHorizontalXS,
  },
  closeBtn: { alignSelf: 'flex-start' },
});

interface SecretRevealBannerProps {
  title: string;
  description: string;
  secrets: Secret[];
  onDismiss: () => void;
  onCopy: (label: string) => void;
}

// Mask uses a fixed-width placeholder so the on-screen length doesn't
// leak the secret's actual length (shoulder-surfing defence).
const SHORT_MASK = '••••••••••••';
const MULTILINE_MASK = '(value hidden — click Show to reveal)';

export default function SecretRevealBanner({
  title,
  description,
  secrets,
  onDismiss,
  onCopy,
}: SecretRevealBannerProps) {
  const styles = useStyles();
  // Per-label visibility. Default to all-hidden; the user explicitly clicks
  // Show on the value they need to inspect, then can Hide again. Copy and
  // Download work regardless — they pull the real value, not the displayed
  // one, so the user doesn't have to reveal to copy.
  const [shown, setShown] = useState<Record<string, boolean>>({});
  const isShown = (label: string) => Boolean(shown[label]);
  const toggle = (label: string) =>
    setShown((prev) => ({ ...prev, [label]: !prev[label] }));

  const handleCopy = (s: Secret) => {
    void navigator.clipboard.writeText(s.value);
    onCopy(s.label);
  };

  const handleDownload = (s: Secret) => {
    if (!s.filename) return;
    const blob = new Blob([s.value], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = s.filename;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className={styles.root} role="alert">
      <div className={styles.iconWrap}>
        <Key20Filled />
      </div>
      <div className={styles.body}>
        <Subtitle2>{title}</Subtitle2>
        <Body1 className={styles.desc}>{description}</Body1>
        {secrets.map((s) => {
          const reveal = isShown(s.label);
          const displayValue = reveal ? s.value : s.multiline ? MULTILINE_MASK : SHORT_MASK;
          return (
            <div key={s.label} className={styles.secretRow}>
              <Body2 className={styles.secretLabel}>{s.label}</Body2>
              <div className={styles.secretValueWrap}>
                <div className={s.multiline ? styles.secretValueMulti : styles.secretValueOneLine}>
                  {displayValue}
                </div>
                <div className={styles.actions}>
                  <Button
                    appearance="subtle"
                    size="small"
                    icon={reveal ? <EyeOff20Regular /> : <Eye20Regular />}
                    onClick={() => toggle(s.label)}
                    aria-label={reveal ? 'Hide value' : 'Show value'}
                  >
                    {reveal ? 'Hide' : 'Show'}
                  </Button>
                  {s.filename && (
                    <Button
                      appearance="subtle"
                      size="small"
                      icon={<ArrowDownload20Regular />}
                      onClick={() => handleDownload(s)}
                    >
                      Download
                    </Button>
                  )}
                  <Button
                    appearance="subtle"
                    size="small"
                    icon={<Copy20Regular />}
                    onClick={() => handleCopy(s)}
                  >
                    Copy
                  </Button>
                </div>
              </div>
            </div>
          );
        })}
      </div>
      <Button
        className={styles.closeBtn}
        appearance="subtle"
        icon={<Dismiss20Regular />}
        onClick={onDismiss}
        aria-label="Dismiss"
      />
    </div>
  );
}
