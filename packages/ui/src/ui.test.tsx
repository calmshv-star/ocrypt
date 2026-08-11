import { fireEvent, render, screen } from "@testing-library/react";
import { LayoutDashboard } from "lucide-react";
import { describe, expect, it, vi } from "vitest";
import { AppShell } from "./app-shell";
import { DataTable } from "./data-table";
import { ThemeProvider } from "./theme";

const labels = {
  skipContent: "Skip",
  openNavigation: "Open navigation",
  closeNavigation: "Close navigation",
  commandMenu: "Command menu",
  searchPlaceholder: "Search records",
  noResults: "No results",
  theme: "Theme",
  notifications: "Notifications",
  account: "Account",
  signOut: "Sign out",
  primaryNavigation: "Primary navigation",
  platformOperational: "Platform operational",
  searchResults: "Search results"
};

describe("shared UI", () => {
  it("renders an accessible shell and persists a theme change", () => {
    const workspaceText = "Workspace";
    const dashboardText = "Dashboard content";
    const storageKey = "merchant-admin-theme-v2-test";
    window.localStorage.removeItem(storageKey);
    render(
      <ThemeProvider defaultTheme="light" storageKey={storageKey}>
        <AppShell
          labels={labels}
          navGroups={[{ label: "Operations", items: [{ href: "#/overview", icon: LayoutDashboard, label: "Overview" }] }]}
          workspace={<span>{workspaceText}</span>}
        >
          <h1>{dashboardText}</h1>
        </AppShell>
      </ThemeProvider>
    );

    expect(screen.getByRole("navigation", { name: labels.primaryNavigation })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: dashboardText })).toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    fireEvent.click(screen.getByRole("button", { name: labels.theme }));
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(window.localStorage.getItem(storageKey)).toBe("dark");
  });

  it("keeps responsive table labels and keyboard-selectable rows", () => {
    const onRowClick = vi.fn();
    const { container } = render(
      <DataTable
        columns={[{ header: "Transfer", key: "id", mobileLabel: "Transfer", render: (row: { id: string }) => row.id }]}
        data={[{ id: "evt_01" }]}
        empty={labels.noResults}
        getRowKey={(row) => row.id}
        nextLabel="Next"
        onRowClick={onRowClick}
        previousLabel="Previous"
        rowsLabel="rows"
      />
    );

    expect(container.querySelector("td")).toHaveAttribute("data-label", "Transfer");
    fireEvent.keyDown(screen.getByText("evt_01").closest("tr")!, { key: "Enter" });
    expect(onRowClick).toHaveBeenCalledWith({ id: "evt_01" });
  });
});
