import type {
  ButtonHTMLAttributes,
  HTMLAttributes,
  InputHTMLAttributes,
  PropsWithChildren,
  SelectHTMLAttributes
} from "react";
import { cn } from "./utils";

export type ButtonVariant = "primary" | "secondary" | "quiet" | "danger";
export type ButtonSize = "sm" | "md" | "lg" | "icon";

export function Button({
  className,
  variant = "primary",
  size = "md",
  type = "button",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: ButtonSize }) {
  return <button className={cn("mp-button", `mp-button--${variant}`, `mp-button--${size}`, className)} type={type} {...props} />;
}

export function IconButton({ label, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return <Button aria-label={label} className={className} size="icon" variant="quiet" {...props} />;
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cn("mp-input", className)} {...props} />;
}

export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className={cn("mp-select", className)} {...props} />;
}

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mp-card", className)} {...props} />;
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mp-card__header", className)} {...props} />;
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h2 className={cn("mp-card__title", className)} {...props} />;
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("mp-card__description", className)} {...props} />;
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mp-card__content", className)} {...props} />;
}

export type StatusTone = "neutral" | "positive" | "warning" | "negative" | "info" | "violet";

export function Badge({ tone = "neutral", className, ...props }: HTMLAttributes<HTMLSpanElement> & { tone?: StatusTone }) {
  return <span className={cn("mp-badge", `mp-badge--${tone}`, className)} {...props} />;
}

export function Avatar({ initials, tone = "indigo", label }: { initials: string; tone?: "indigo" | "teal" | "amber" | "rose"; label?: string }) {
  return <span aria-label={label} className={cn("mp-avatar", `mp-avatar--${tone}`)} role={label ? "img" : undefined}>{initials}</span>;
}

export function VisuallyHidden({ children }: PropsWithChildren) {
  return <span className="mp-sr-only">{children}</span>;
}

export function Skeleton({ className }: { className?: string }) {
  return <span aria-hidden="true" className={cn("mp-skeleton", className)} />;
}
