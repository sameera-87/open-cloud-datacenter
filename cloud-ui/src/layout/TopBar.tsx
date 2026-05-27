import {
  Avatar,
  Button,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Tooltip,
  makeStyles,
  shorthands,
  tokens,
} from '@fluentui/react-components';
import {
  Question20Regular,
  Alert20Regular,
  WeatherMoon20Regular,
  WeatherSunny20Regular,
} from '@fluentui/react-icons';
import { useAuth } from '../auth/useAuth';
import TenantSwitcher from './TenantSwitcher';
import ProjectSwitcher from './ProjectSwitcher';

const useStyles = makeStyles({
  bar: {
    height: '48px',
    display: 'flex',
    alignItems: 'center',
    paddingLeft: tokens.spacingHorizontalL,
    paddingRight: tokens.spacingHorizontalL,
    gap: tokens.spacingHorizontalM,
    backgroundColor: tokens.colorNeutralBackground1,
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
    position: 'sticky',
    top: 0,
    zIndex: 100,
  },
  brand: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    minWidth: '224px',
    paddingRight: tokens.spacingHorizontalM,
    ...shorthands.borderRight('1px', 'solid', tokens.colorNeutralStroke2),
    height: '100%',
  },
  brandMark: {
    width: '28px',
    height: '28px',
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorBrandBackground,
    color: tokens.colorNeutralForegroundOnBrand,
    display: 'grid',
    placeItems: 'center',
    fontWeight: 700,
    fontSize: tokens.fontSizeBase300,
  },
  brandText: {
    display: 'flex',
    flexDirection: 'column',
    lineHeight: 1.1,
    gap: '2px',
  },
  brandTitle: { fontSize: tokens.fontSizeBase300, fontWeight: 600 },
  brandSub: { fontSize: tokens.fontSizeBase100, color: tokens.colorNeutralForeground3 },
  spacer: { flex: 1 },
  rightGroup: { display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalXS },
});

/** Derive a display name and two-letter initials from an email or sub. */
function deriveDisplayName(email?: string, sub?: string): { name: string; initials: string } {
  const source = email?.trim() || sub?.trim() || '';
  const name = email ? email.split('@')[0] : (sub ?? 'User');
  const parts = name.split(/[\s._-]+/).filter(Boolean);
  const initials = (
    (parts[0]?.[0] ?? '') + (parts.length > 1 ? (parts.at(-1)?.[0] ?? '') : (parts[0]?.[1] ?? ''))
  )
    .toUpperCase()
    .slice(0, 2) || 'U';
  return { name: source || 'User', initials };
}

interface TopBarProps {
  isDark: boolean;
  onToggleDark: (next: boolean) => void;
}

export default function TopBar({ isDark, onToggleDark }: TopBarProps) {
  const styles = useStyles();
  const { user, logout } = useAuth();
  const { name, initials } = deriveDisplayName(user?.email, user?.sub);

  return (
    <header className={styles.bar}>
      <div className={styles.brand}>
        <div className={styles.brandMark}>W</div>
        <div className={styles.brandText}>
          <div className={styles.brandTitle}>Sovereign Cloud</div>
          <div className={styles.brandSub}>lk-dev</div>
        </div>
      </div>

      <TenantSwitcher />
      <ProjectSwitcher />

      <div className={styles.spacer} />

      <div className={styles.rightGroup}>
        <Tooltip content={isDark ? 'Switch to light mode' : 'Switch to dark mode'} relationship="label">
          <Button
            appearance="subtle"
            icon={isDark ? <WeatherSunny20Regular /> : <WeatherMoon20Regular />}
            onClick={() => onToggleDark(!isDark)}
          />
        </Tooltip>
        <Tooltip content="Help" relationship="label">
          <Button appearance="subtle" icon={<Question20Regular />} />
        </Tooltip>
        <Tooltip content="Notifications" relationship="label">
          <Button appearance="subtle" icon={<Alert20Regular />} />
        </Tooltip>

        <Menu>
          <MenuTrigger disableButtonEnhancement>
            <Button appearance="subtle" aria-label={name}>
              <Avatar size={28} name={name} initials={initials} color="brand" />
            </Button>
          </MenuTrigger>
          <MenuPopover>
            <MenuList>
              <MenuItem disabled>{user?.email ?? user?.sub ?? 'not signed in'}</MenuItem>
              <MenuItem onClick={() => void logout()}>Sign out</MenuItem>
            </MenuList>
          </MenuPopover>
        </Menu>
      </div>
    </header>
  );
}
