import * as Dialog from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import * as Tooltip from "@radix-ui/react-tooltip";
import {
  Bell,
  ChevronDown,
  ChevronsUpDown,
  Command,
  Menu,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  Sun,
  X,
  type LucideIcon
} from "lucide-react";
import {
  type PropsWithChildren,
  type ReactNode,
  useEffect,
  useMemo,
  useState
} from "react";
import { PRODUCT_NAME, PRODUCT_SHORT_NAME } from "./brand";
import { Avatar, Badge, Button, IconButton, Input } from "./primitives";
import { useTheme } from "./theme";
import { cn } from "./utils";

export type ShellNavItem = {
  label: string;
  href: string;
  icon: LucideIcon;
  active?: boolean;
  badge?: string | number;
  keywords?: string[];
};

export type ShellNavGroup = {
  label: string;
  items: ShellNavItem[];
  collapsible?: boolean;
};

type SignOutHandler = () =>
  Promise<void> |
  void;

export type ShellLabels = {
  skipContent: string;
  openNavigation: string;
  closeNavigation: string;
  commandMenu: string;
  searchPlaceholder: string;
  noResults: string;
  theme: string;
  notifications: string;
  account: string;
  signOut: string;
  signingOut?: string;
  primaryNavigation: string;
  searchResults: string;
};

type AppShellProps = PropsWithChildren<{
  navGroups: ShellNavGroup[];
  labels: ShellLabels;
  environment?: ReactNode;
  workspace?: ReactNode;
  headerStart?: ReactNode;
  headerEnd?: ReactNode;
  user?: { name: string; email: string; initials: string };
  onNavigate?: () => void;
  onSignOut?: SignOutHandler;
}>;

function Brand({ collapsed = false }: { collapsed?: boolean }) {
  return (
    <a aria-label={PRODUCT_NAME} className="mp-brand" href="#/overview">
      <span aria-hidden="true" className="mp-brand__mark">
        <span />
        <span />
      </span>
      <span className="mp-brand__name">{collapsed ? PRODUCT_SHORT_NAME : PRODUCT_NAME}</span>
    </a>
  );
}

function SidebarContent({
  navGroups,
  collapsed,
  workspace,
  environment,
  onNavigate,
  labels
}: Pick<AppShellProps, "navGroups" | "workspace" | "environment" | "onNavigate" | "labels"> & { collapsed?: boolean }) {
  return (
    <>
      <div className="mp-sidebar__brand-row">
        <Brand collapsed={collapsed} />
      </div>
      <div className="mp-sidebar__workspace">
        {workspace}
        {!collapsed && environment}
      </div>
      <nav aria-label={labels.primaryNavigation} className="mp-sidebar__nav">
        {navGroups.map((group) => {
          const items = group.items.map((item) => {
              const Icon = item.icon;
              const itemKey = `${group.label}:${item.href}:${item.label}`;
              const link = (
                <a
                  aria-current={item.active ? "page" : undefined}
                  className={cn("mp-nav-item", item.active && "is-active", collapsed && "is-collapsed")}
                  href={item.href}
                  key={itemKey}
                  onClick={onNavigate}
                >
                  <Icon aria-hidden="true" size={18} strokeWidth={1.8} />
                  {!collapsed && <span>{item.label}</span>}
                  {!collapsed && item.badge !== undefined && <Badge tone={item.active ? "info" : "neutral"}>{item.badge}</Badge>}
                </a>
              );
              return collapsed ? (
                <Tooltip.Root delayDuration={120} key={itemKey}>
                  <Tooltip.Trigger asChild>{link}</Tooltip.Trigger>
                  <Tooltip.Portal><Tooltip.Content className="mp-tooltip" side="right" sideOffset={8}>{item.label}</Tooltip.Content></Tooltip.Portal>
                </Tooltip.Root>
              ) : link;
            });
          if (group.collapsible && !collapsed) return <details className="mp-nav-group" key={group.label} open={group.items.some((item) => item.active)}><summary className="mp-nav-group__label">{group.label}</summary>{items}</details>;
          return <div className="mp-nav-group" key={group.label}>{!collapsed && <p className="mp-nav-group__label">{group.label}</p>}{items}</div>;
        })}
      </nav>
    </>
  );
}

export function ThemeToggle({ label }: { label: string }) {
  const { resolvedTheme, toggleTheme } = useTheme();
  return (
    <IconButton label={label} onClick={toggleTheme}>
      {resolvedTheme === "dark" ? <Sun aria-hidden="true" size={18} /> : <Moon aria-hidden="true" size={18} />}
    </IconButton>
  );
}

export function AppShell({
  children,
  navGroups,
  labels,
  workspace,
  environment,
  headerStart,
  headerEnd,
  user = { name: "Alex Morgan", email: "alex@merchant.example", initials: "AM" },
  onNavigate,
  onSignOut
}: AppShellProps) {
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const notificationItems = useMemo(() => {
    const preferred = new Set(["#/unmatched", "#/webhooks", "#/management-actions"]);
    return navGroups.flatMap((group) => group.items).filter((item) => preferred.has(item.href));
  }, [navGroups]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setCommandOpen((value) => !value);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const handleNavigate = () => {
    setMobileOpen(false);
    onNavigate?.();
  };
  const handleSignOut = async () => {
    if (!onSignOut || signingOut) return;
    setSigningOut(true);
    try {
      await onSignOut();
    } catch {
      // The authenticated shell remains mounted so the operator can retry.
    } finally {
      setSigningOut(false);
    }
  };

  return (
    <Tooltip.Provider>
      <a className="mp-skip-link" href="#main-content">{labels.skipContent}</a>
      <div className={cn("mp-shell", collapsed && "is-collapsed")}>
        <aside className="mp-sidebar">
          <SidebarContent collapsed={collapsed} environment={environment} labels={labels} navGroups={navGroups} onNavigate={handleNavigate} workspace={workspace} />
          <IconButton
            className="mp-sidebar__collapse"
            label={collapsed ? labels.openNavigation : labels.closeNavigation}
            onClick={() => setCollapsed((value) => !value)}
          >
            {collapsed ? <PanelLeftOpen size={17} /> : <PanelLeftClose size={17} />}
          </IconButton>
        </aside>

        <header className="mp-topbar">
          <div className="mp-topbar__start">
            <IconButton className="mp-mobile-menu" label={labels.openNavigation} onClick={() => setMobileOpen(true)}>
              <Menu aria-hidden="true" size={20} />
            </IconButton>
            {headerStart}
          </div>
          <div className="mp-topbar__actions">
            <Button aria-label={labels.commandMenu} className="mp-command-trigger" onClick={() => setCommandOpen(true)} variant="quiet">
              <Search aria-hidden="true" size={16} />
              <span>{labels.searchPlaceholder}</span>
              <kbd><Command size={11} />{"K"}</kbd>
            </Button>
            {headerEnd}
            <ThemeToggle label={labels.theme} />
            <DropdownMenu.Root>
              <DropdownMenu.Trigger asChild>
                <IconButton label={labels.notifications}><Bell aria-hidden="true" size={18} /></IconButton>
              </DropdownMenu.Trigger>
              <DropdownMenu.Portal>
                <DropdownMenu.Content align="end" className="mp-menu mp-notification-menu" sideOffset={8}>
                  <div className="mp-menu__profile"><strong>{labels.notifications}</strong></div>
                  <DropdownMenu.Separator className="mp-menu__separator" />
                  {notificationItems.length === 0 && <p className="mp-notification-menu__empty">{labels.noResults}</p>}
                  {notificationItems.map((item) => {
                    const Icon = item.icon;
                    return <DropdownMenu.Item asChild key={item.href}><a className="mp-menu__item mp-notification-menu__item" href={item.href} onClick={handleNavigate}><Icon aria-hidden="true" size={16} /><span>{item.label}</span>{item.badge !== undefined && <Badge tone="neutral">{item.badge}</Badge>}</a></DropdownMenu.Item>;
                  })}
                </DropdownMenu.Content>
              </DropdownMenu.Portal>
            </DropdownMenu.Root>
            <DropdownMenu.Root>
              <DropdownMenu.Trigger asChild>
                <button aria-label={labels.account} className="mp-user-trigger">
                  <Avatar initials={user.initials} />
                  <span className="mp-user-trigger__text"><strong>{user.name}</strong><small>{user.email}</small></span>
                  <ChevronDown aria-hidden="true" size={14} />
                </button>
              </DropdownMenu.Trigger>
              <DropdownMenu.Portal>
                <DropdownMenu.Content align="end" className="mp-menu" sideOffset={8}>
                  <div className="mp-menu__profile"><strong>{user.name}</strong><span>{user.email}</span></div>
                  <DropdownMenu.Separator className="mp-menu__separator" />
                  <DropdownMenu.Item className="mp-menu__item" disabled>{labels.account}</DropdownMenu.Item>
                  <DropdownMenu.Item aria-busy={signingOut || undefined} className="mp-menu__item" disabled={!onSignOut || signingOut} onSelect={() => void handleSignOut()}>{signingOut ? labels.signingOut ?? labels.signOut : labels.signOut}</DropdownMenu.Item>
                </DropdownMenu.Content>
              </DropdownMenu.Portal>
            </DropdownMenu.Root>
          </div>
        </header>

        <main className="mp-main" id="main-content" tabIndex={-1}>{children}</main>
      </div>

      <Dialog.Root onOpenChange={setMobileOpen} open={mobileOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="mp-dialog-overlay" />
          <Dialog.Content aria-describedby={undefined} className="mp-mobile-drawer">
            <Dialog.Title className="mp-sr-only">{labels.openNavigation}</Dialog.Title>
            <Dialog.Close asChild><IconButton className="mp-mobile-drawer__close" label={labels.closeNavigation}><X size={20} /></IconButton></Dialog.Close>
            <SidebarContent environment={environment} labels={labels} navGroups={navGroups} onNavigate={handleNavigate} workspace={workspace} />
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>

      <CommandPalette labels={labels} navGroups={navGroups} onOpenChange={setCommandOpen} open={commandOpen} />
    </Tooltip.Provider>
  );
}

function CommandPalette({
  open,
  onOpenChange,
  navGroups,
  labels
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  navGroups: ShellNavGroup[];
  labels: ShellLabels;
}) {
  const [query, setQuery] = useState("");
  const items = useMemo(() => navGroups.flatMap((group) => group.items), [navGroups]);
  const visibleItems = items.filter((item) =>
    [item.label, ...(item.keywords ?? [])].join(" ").toLowerCase().includes(query.toLowerCase())
  );

  useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  return (
    <Dialog.Root onOpenChange={onOpenChange} open={open}>
      <Dialog.Portal>
        <Dialog.Overlay className="mp-dialog-overlay" />
        <Dialog.Content className="mp-command-dialog">
          <Dialog.Title className="mp-sr-only">{labels.commandMenu}</Dialog.Title>
          <div className="mp-command-dialog__search">
            <Search aria-hidden="true" size={18} />
            <Input autoFocus onChange={(event) => setQuery(event.target.value)} placeholder={labels.searchPlaceholder} value={query} />
            <Dialog.Close asChild><IconButton label={labels.closeNavigation}><X size={18} /></IconButton></Dialog.Close>
          </div>
          <div aria-label={labels.searchResults} className="mp-command-dialog__results" role="listbox">
            {visibleItems.length === 0 && <p className="mp-command-dialog__empty">{labels.noResults}</p>}
            {visibleItems.map((item) => {
              const Icon = item.icon;
              return (
                <a className="mp-command-result" href={item.href} key={item.href} onClick={() => onOpenChange(false)} role="option">
                  <span><Icon size={17} />{item.label}</span><span>↵</span>
                </a>
              );
            })}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function WorkspaceSwitcher({
  icon,
  name,
  detail,
  label,
  onClick
}: {
  icon?: ReactNode;
  name: string;
  detail: string;
  label?: string;
  onClick?: () => void;
}) {
  return (
    <button aria-label={label} className="mp-workspace-switcher" onClick={onClick} type="button">
      <span className="mp-workspace-switcher__icon">{icon ?? <span aria-hidden="true">{PRODUCT_SHORT_NAME}</span>}</span>
      <span><strong>{name}</strong><small>{detail}</small></span>
      <ChevronsUpDown aria-hidden="true" size={14} />
    </button>
  );
}
