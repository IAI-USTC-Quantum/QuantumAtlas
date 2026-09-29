<!-- page 1 of 17 -->

arXiv:1605.01488v2 [cs.DS] 26 Jun 2016

# Fully dynamic data structure for LCE queries in compressed space

**Takaaki Nishimoto**<strong><sup>1</sup></strong> **Tomohiro I**<strong><sup>2</sup></strong> **Shunsuke Inenaga**<strong><sup>1</sup></strong>

**Hideo Bannai**<strong><sup>1</sup></strong> **Masayuki Takeda**<strong><sup>1</sup></strong>

**1 Department of Informatics, Kyushu University**

{takaaki.nishimoto,inenaga,bannai,takeda}@inf.kyushu-u.ac.jp

**2 Kyushu Institute of Technology, Japan**

tomohiro@ai.kyutech.ac.jp

## Abstract

**A Longest Common Extension (LCE) query on a textT of lengthN asks for the length of the longest common prefix of suffixes starting at given two positions. We show that the signature encoding**G **of size**w=**O(min(z logN log**<strong><sup>∗</sup></strong> **M, N)) [Mehlhorn et al., Algorithmica 17(2):183-198, 1997] ofT , which can be seen as a compressed representation ofT , has a capability to support LCE queries inO(logN + logℓ log∗M) time, whereℓ is the answer to the query,z is the size of the Lempel-Ziv77 (LZ77) factorization ofT , and**M≥4**N is an integer that can be handled in constant time under word RAM model. In compressed space, this is the fastest deterministic LCE data structure in many cases. Moreover,**G **can be enhanced to support efficient update operations: After processing**G **in** $O ( w f _ { \mathcal { A } } )$ time, we can insert/delete any (sub)string of lengthy into/from an arbitrary position ofT in O((**y + logN log∗**M)**fA) time, where** $\textstyle f _ { \mathcal { A } } = O ( \operatorname* { m i n } \{ \frac { \operatorname { l o g } \operatorname { l o g } M \operatorname { l o g } \operatorname { l o g } w } { \operatorname { l o g } \operatorname { l o g } \operatorname { l o g } M } , \sqrt { \frac { \operatorname { l o g } w } { \operatorname { l o g } \operatorname { l o g } w } } \} )$ . This yields the first fully dynamic LCE data structure working in compressed space. We also present efficient construction algorithms from various types of inputs: We can constructG inO(N**fA**) time from uncompressed stringT ; inO(n log log(n logM) logN logM) time from grammar-compressed stringT represented by a straight-line program of sizen; and in $O ( z f _ { \mathcal { A } }$ log N log<sup>∗</sup>M) time from LZ77-compressed string T withz factors. On top of the above contributions, we show several applications of our data structures which improve previous best known results on grammar-compressed **string** processing.

## 1 Introduction

**A Longest Common Extension (LCE) query on a textT of lengthN asks to compute the length of the longest common prefix of suffixes starting at given two positions. This fundamental query appears at the heart of many string processing problems (see text book [11] for example), and hence, efficient data structures to answer LCE queries gain a great attention. A classic solution is to use a data structure for lowest common ancestor queries [4] on the suffix tree ofT . Although this achieves constant query time, the Θ(N) space needed for the data structure is too large to apply it to large scale data. Hence, recent work focuses on reducing space usage at the expense of query time. For example, time-space trade-offs of LCE data structure have been extensively studied [7, 24].**

**Another direction to reduce space is to utilize a compressed structure of T , which is advantageous whenT is highly compressible. There are several LCE data structures working on grammar-compressed stringT represented by a straight-line program (SLP) of sizen. The best known deterministic LCE data structure is due to I et al. [13], which supports LCE queries in**O(**h logN) time, and occupies** $O ( n ^ { 2 } )$ space, whereh is the height of the derivation tree of a given SLP. Their data structure can be built in $O ( h n ^ { 2 } )$ time directly from the SLP. Bille et al. [5] showed a Monte Carlo randomized data structure which supports LCE queries inO(logN logℓ) time, whereℓ is the output of the LCE query. Their data structure requires onlyO(n) space, but requiresO(N) time to construct. Very recently, Bille et al. [6] showed a faster Monte Carlo randomized data structure of $O ( n )$ space **which supports LCE queries in** $O ( \log N + \log ^ { 2 } \ell )$ **time. The preprocessing time of this new data structure is not given in [6]. Note that, given the LZ77-compression of sizez ofT , we can convert it into an SLP of size** $\begin{array} { r } { n = O ( z \log \frac { N } { z } ) } \end{array}$ [22] and **then apply the above results.**

**In this paper, we focus on the signature encoding G of T , which can be seen as a grammar compression ofT , and show thatG can support LCE queries efficiently. The signature encoding was proposed by**

1

<!-- page 2 of 17 -->

**Mehlhorn et al. for equality testing on a dynamic set of strings [19]. Alstrup et al. used signature encodings combined with their own data structure called anchors to present a pattern matching algorithm on a dynamic set of strings [2, 1]. In their paper, they also showed that signature encodings can support longest common prefix (LCP) and longest common suffix (LCS) queries on a dynamic set of strings. Their algorithm is randomized as it uses a hash table for maintaining the dictionary of G. Very recently, Gawrychowski et al. improved the results by pursuing advantages of randomized approach other than the hash table [10]. It should be noted that the algorithms in [2, 1, 10] can support LCE queries by combining split operations and LCP queries although it is not explicitly mentioned. However, they did not focus on the fact that signature encodings can work in compressed space. In [9], LCE data structures on edit sensitive parsing, a variant of signature encoding, was used for sparse suffix sorting, but again, they did not focus on working in compressed space.**

**Our contributions are stated by the following theorems, where** $M \geq 4 N$ **is an integer that can be handled in constant time under word RAM model. More specifically, M = 4N if T is static, and** $M / 4$ **is the upper bound of the length of T if we consider updating T dynamically. In dynamic case, N (resp. w) always denotes the current size of T (resp. G). Also,** $f _ { A }$ **denotes the time for predecessor/successor queries** on a set of w integers from an M-element universe, which is $\textstyle f _ { \mathcal { A } } { = } O ( \operatorname* { m i n } \{ \frac { \operatorname { l o g } \operatorname { l o g } M \operatorname { l o g } \operatorname { l o g } w } { \operatorname { l o g } \operatorname { l o g } \operatorname { l o g } M } , \sqrt { \frac { \operatorname { l o g } w } { \operatorname { l o g } \operatorname { l o g } w } } \} )$ **by the best known data structure [3].**

**Theorem 1 (LCE queries). Let G denote the signature encoding of size w = O(min(z log N** log<sup>∗</sup> M, N)) for a string T of length N. Then G supports LCE queries on T in $O ( \operatorname { l o g } N + \operatorname { l o g } \ell \operatorname { l o g } ^ { * } M )$ **time, where ℓ is the answer to the query, and z is the size of the LZ77 factorization of T .**

**Theorem 2 (Updates). After processing G in** $O ( w f _ { \mathcal { A } } )$ **time, we can insert/delete any (sub)string Y of length y into/from an arbitrary position of T in** $O ( ( y +$ **log N log** ${ } ^ { * }   M ) f _ { \mathcal { A } } )$ **time. If Y is given as a substring of T , we can support insertion in** $O ( f _ { \mathcal { A } }$ $N \log ^ { * } M )$ **time.**

**Theorem 3 (Construction). Let T be a string of length N, Z be LZ77 factorization without self reference of size z representing T , and S be an SLP of size n generating T . Then, we can construct** the signature encoding G for T in (1a) in $O ( N f _ { \mathcal { A } } )$ time and O(w) working space from T , (1b) in $O ( N )$ time and working space from T , (2) in $O ( z f _ { \mathcal { A } }$ **log N log**<strong><sup>∗</sup></strong> **M) time and O(w) working space from Z, (3a) in O(nf**<strong><sub>A</sub></strong> **log N log**<strong><sup>∗</sup></strong> **M) time and O(w) working space from S, and (3b) in O(n log log(n log**<strong><sup>∗</sup></strong> **M) log N log**<strong><sup>∗</sup></strong> **M) time and** $O ( n \log ^ { * } M + w )$ **working space from S.**

**The remarks on our contributions are listed in the following:**

**• We achieve an algorithm for the fastest deterministic LCE queries on SLPs, which even permits** faster LCE queries than the randomized data structure of Bille et al. [6] when log $M = o ( \log \ell )$ **which in many cases is true.**

**• We present the first fully dynamic LCE data structure working in compressed space.**

**• Different from the work in [2, 1, 10], we mainly focus on maintaining a single text T in compressed O(w) space. For this reason we opt for supporting insertion/deletion as edit operations rather than split/concatenate on a dynamic set of strings. However, the difference is not much essential; our insert operations specified by a substring of an existing string can work as split/concatenate, and conversely, split/concatenate can simulate insert. Our contribution here is to clarify how to collect garbage being produced during edit operations, as directly indicated by a support of delete operations.**

**• The results (2) and (3a) of Theorem 3 immediately follow from the update operations considered in [2, 1], but others are nontrivial.**

**• Direct construction of G from SLPs is important for applications in compressed string processing, where the task is to process a given compressed representation of string(s) without explicit decompression. In particular, we use the result (3b) of Theorem 3 to show several applications which improve previous best known results. Note that the time complexity of the result (3b) can be written as O(n log log n log** $N \log ^ { * } M )$ **when** $\log ^ { * } M   =   O ( n )$ **which in many cases is true, and** always true in static case because log $\overset { \cdot } { M } = O ( \operatorname { l o g } ^ { * } N ) = O ( \overset { \cdot \cdot \cdot } { \operatorname { l o g } } N ) = O ( n )$

**Proofs and examples omitted due to lack of space are in a full version of this paper [21].**

2

<!-- page 3 of 17 -->

## 2 Preliminaries

## 2.1 Strings

Let Σ be an ordered alphabet. An element of $\Sigma ^ { * }$ is called a string. For string $w   =   x y z , \; x , \; y$ **and z are called a prefix, substring, and suffix of** $w ,$ **respectively. The length of string w is denoted by |w|. The empty string ε is a string of length 0. Let** $\Sigma ^ { + }   =   \Sigma ^ { * }   -   \{ \varepsilon \}$ **. For any** $1   \leq   i   \leq   | w |$ **, w[i] denotes the i-th character of** $w .$ **For any** $1   \leq   i   \leq   j   \leq   | w |$ **, w[i..j] denotes the substring of w that begins at position i and ends at position** $j .$ **Let** $w [ i . . ]   =   w [ i . . | w | ]$ **and** $w [ . . i ]   =   w [ 1 . . i ]$ **for any** $1 \leq i \leq | w |$ **For** any string w, let $w ^ { R }$ denote the reversed string of w, that is, $\vec { w ^ { R } } = w [ | \vec { w } | ] \cdots w [ 2 ] w [ 1 ]$ **. For any strings w and u, let** $\mathsf { L C P } ( w , u )$ **(resp.** $\mathsf { L C S } ( w , u ) )$ **denote the length of the longest common prefix (resp. suffix) of w and u. Given two strings** $s _ { 1 } , s _ { 2 }$ **and two integers** $i , j$ **, let** $\mathsf { L C E } ( s _ { 1 } , s _ { 2 } , i , j )$ **denote a query which returns** $\mathsf { L C P } ( s _ { 1 } [ i . . | s _ { 1 } | ] , s _ { 2 } [ j . . | s _ { 2 } | ] )$ **. Our model of computation is the unit-cost word RAM with machine word size of** $\Omega ( \log _ { 2 } M )$ **bits, and space complexities will be evaluated by the number of machine words. Bit-oriented evaluation of space complexities can be obtained with a log**<strong><sub>2</sub></strong> **M multiplicative factor.**

**Definition 4 (Lempel-Ziv77 factorization [25]). The Lempel-Ziv77 (LZ77) factorization of a string s without self-references is a sequence** $f _ { 1 } , \ldots , f _ { z }$ **of non-empty substrings of s such that** $s \; = \; f _ { 1 } \cdots f _ { z } ,$ $f _ { 1 } \; = \; s [ 1 ]$ **,** and for $1 \; < \; i \; \leq \; z ,$ **if the character** $s [ | f _ { 1 } . . f _ { i - 1 } | + 1 ]$ **does not occur in** $s [ | f _ { 1 } . . f _ { i - 1 } | ]$ **, then** $f _ { i } = s [ | f _ { 1 } . . f _ { i - 1 } | + 1 ]$ **, otherwise** $f _ { i }$ **i**s the longest prefix of $f _ { i } \cdots f _ { z }$ **which occurs in** $f _ { 1 } \cdots f _ { i - 1 }$ **. The size of the LZ77 factorization** $f _ { 1 } , \ldots , f _ { z }$ **of string s is the number z of factors in the factorization.**

## 2.2 Context free grammars as compressed representation of strings

**Straight-line programs. A straight-line program (SLP) is a context free grammar in the Chomsky** normal form that generates a single string. Formally, an SLP that generates T is a quadruple $\mathcal { G } =$ $( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **, such that Σ is an ordered alphabet of terminal characters;** $\mathcal { V } \: = \: \{ X _ { 1 } , \ldots , X _ { n } \}$ **is a set** of positive integers, called variables; $\mathcal { D }   =   \{ X _ { i }   \rightarrow   e x p r _ { i } \} _ { i = 1 } ^ { n }$ **is a set of deterministic productions (or assignments) with each** $e x p r _ { i }$ **being either of form** $X _ { \ell } X _ { r } ( 1 \leq \ell , r < i )$ **, or a single character** $a \in \Sigma ;$ **and** $S : = X _ { n } \in { \mathcal { V } }$ **is the start symbol which derives the string** $T .$ **We also assume that the grammar neither contains redundant variables (i.e., there is at most one assignment whose righthand side is expr ) nor useless variables** $( \mathrm { i . e . }$ **, every variable appears at least once in the derivation tree of** $\mathcal { G } )$ **. The size of the SLP** $\mathcal { G }$ **is the number n of productions in D. In the extreme cases the length N of the string** $T$ **can be** as large as $2 ^ { n - 1 }$ , however, it is always the case that $n \geq \log _ { 2 } N$

Let val $: \mathcal { V } \to \Sigma ^ { + }$ be the function which returns the string derived by an input variable. $\operatorname { I f } s = v a l ( X )$ for $X \in \mathcal { V }$ **, then we say that the variable X represents string s. For any variable sequence** $y \in \mathcal { V } ^ { + }$ **, let** val $l ^ { + } ( y ) = { \it v a l } ( y [ 1 ] ) \cdots { \it v a l } ( y [ | y | ] )$

**Run-length straight-line programs. We define run-length** $\mathit { S L P s } \quad ( \mathit { R L S L P s } )$ **, as an extension to** $\mathrm { S L P s } ,$ **which allow run-length encodings in the righthand sides of productions,** $\mathrm { i . e . , } ~ \mathcal { D }$ **might contain a production** $X \to { \hat { X } } ^ { k } \in { \mathcal { V } } \mathbin { \dot { \times } } { \mathcal { N } } .$ **The size of the RLSLP is still the number of productions in D as each** production can be encoded in constant space. Let $A s s g n _ { \mathcal { G } }$ be the function such that $\mathit { A s s g n } _ { \mathcal { G } } ( X _ { i } ) = \mathit { e x p r } _ { i }$ iff $X _ { i } \to \mathit { e x p r } _ { i } \in \mathcal { D }$ . Also, let $\boldsymbol { A s s g n _ { \mathcal { G } } ^ { - 1 } }$ denote the reverse function of $A s s g n _ { \mathcal { G } }$ **. When clear from the** context, we write $A s s g n _ { \mathcal { G } }$ and $A s s g n _ { \mathcal { G } } ^ { - \dot { 1 } }$ as Assgn and $A s s g n ^ { - 1 }$ **, respectively.**

**Representation of RLSLPs. For an RLSLP G of size** $w ,$ **we can consider a DAG of size w as a compact representation of the derivation trees of variables in** $\mathcal { G } .$ **Each node represents a variable X in V and store** $| v a l ( X ) |$ **and out-going edges represent the assignments in** $\mathcal { D } ;$ **For an assignment** $X _ { i } \to X _ { \ell } X _ { r } \in \mathcal { D }$ **, there exist two out-going edges from** $X _ { i }$ **to its ordered children** $X _ { \ell }$ **a**nd $X _ { r } ;$ and for $X \to { \hat { X } } ^ { k } \in { \mathcal { D } }$ , there is a **single edge from X to** $\hat { X }$ **with the multiplicative factor k.**

## 3 Signature encoding

**Here, we recall the signature encoding first proposed by Mehlhorn et al. [19]. Its core technique is locally consistent parsing defined as follows:**

**Lemma 5 (Locally consistent parsing [19, 1]). Let W be a positive integer. There exists a function** $f   :   [ 0 . . W ] ^ { \log ^ { * } W + 1 1 ^ { \dot { } } }   \rightarrow   \{ 0 , 1 \}$ such that, for any $p   \in   [ 1 . . W ] ^ { n }$ **with** $n \geq 2$ **and** $p [ i ]   \neq   p [ i   +   1 ]$ **for any** $1   \leq   i   <   n$ **, the bit sequence d defined by** $d [ i ]   =   f ( \tilde { p } [ i   -   \Delta _ { L } ] , \ldots , \tilde { p } [ i   +   \Delta _ { R } ] )$ **for** $1   \leq   i   \leq   n$ **, satisfies:**

3

<!-- page 4 of 17 -->

$d [ 1 ] = 1 ;   d [ n ] = 0 ;   d [ i ] + d [ i + 1 ] \leq 1$ **for** $1 \leq i < n ;$ **and** $d [ i ] + d [ i + 1 ] + d [ i + 2 ] + d [ i + 3 ] \geq 1$ **for any** $1 \leq i < n - 3 ;$ **where** $\Delta _ { L } = \operatorname { l o g } ^ { * } W + 6 , \: \Delta _ { R } = 4 ,$ **, and** $\tilde { p } [ j ] = p [ j ]$ **for all** $1 \leq j \leq n ,   \tilde { p } [ j ] = 0$ **otherwise. Furthermore, we can compute d in** $O ( n )$ **time using a precomputed table of size** $o ( \log W )$ **, which can be computed in o(log W) time.**

**For the bit sequence d of Lemma 5, we define the function** ${ \mathit { E b l o c k } } _ { d } ( p )$ **that decomposes an integer sequence p according to d:** $\mathit { E b l o c k } _ { d } ( p )$ **decomposes p into a sequence** $q _ { 1 } , \ldots , q _ { j }$ **of substrings called blocks of** $p ,$ **such that** $p = q _ { 1 } \cdots q _ { j }$ **and** $q _ { i }$ **i**s in the decomposition iff $d [ | q _ { 1 } \cdots q _ { i - 1 } | \stackrel { \circ } { + } 1 ] = 1$ for any $1 \leq i \leq j$ Note that each block is of length from two to four by the property of d, $i.e.,   2 \leq |q_i| \leq 4$ for any $1 \leq i \leq j$ Let $| \mathit { E b l o c k } _ { d } ( p ) | = j$ and let $\mathit { E b l o c k } _ { d } ( s ) [ i ] = q _ { i }$ **.** We omit d and write $E b l o c k ( p )$ **when it is clear from the** context, and we use implicitly the bit sequence created by Lemma 5 as $d .$

**We complementarily use run-length encoding to get a sequence to which Eblock can be applied. Formally, for a string s, let** $E p o w ( s )$ **be the function which groups each maximal run of same characters** a as $a ^ { k }$ , where k is the length of the run. $E p o w ( s )$ **can be computed in** $O ( | s | )$ **time. Let** $| E p o w ( s )$ **denote the number of maximal runs of same characters in s and let** $\mathit { E p o w } ( s ) [ i ]$ **denote i-th maximal run in s.**

**The signature encoding is the RLSLP** $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **, where the assignments in D are determined by recursively applying Eblock and Epow to** $T$ **until a single integer S is obtained. We call each variable of the signature encoding a signature, and use e (for example,** $e _ { i } \to e _ { \ell } e _ { r } \in \mathcal { D } )$ **instead of X to distinguish from general RLSLPs.**

**For a formal description, let** $E \; : = \; \Sigma \cup \mathcal { V } ^ { 2 } \cup \mathcal { V } ^ { 3 } \cup \mathcal { V } ^ { 4 } \cup ( \mathcal { V } \times \mathcal { N } )$ **and let** $\mathit { S i g } : E \to \mathcal { V }$ **be the** function such that: $\widetilde { \mathit { S i g } ( x ) } \: = \: e \: \operatorname { i f } \: ( e \: \to \: x ) \: \in \: \mathcal { D } ; \: \mathit { S i g } ( x ) \: = \: \widetilde { \mathit { S i g } ( \mathit { S i g } ( x [ 1 . . | x | - 1 ] ) x [ | x | ] ) } \: \operatorname { i f } \: x \: \in \: \mathcal { V } ^ { 3 } \cup \mathcal { V } ^ { 4 } ;$ **or otherwise undefined. Namely, the function Sig returns, if any, the lefthand side of the corresponding** production of x by recursively applying the $\dot { A s s g n } ^ { - 1 }$ function from left to right. For any $p   \in   E ^ { * }$ **, let** $\dot { \mathit { S i g } } ^ { + } ( p ) = \mathit { S i g } ( p [ 1 ] ) \cdots \mathit { S i g } ( p [ \dot { | p | } ] )$

The signature encoding of string T is defined by the following Shrink and Pow functions: $\mathit { S h r i n k } _ { t } ^ { T } =$ $\mathit { S i g } ^ { + } ( T )$ for $t = 0 .$ , and $\tilde { \mathit { S h r i n k } } _ { t } ^ { T } = \tilde { \mathit { S i g } } ^ { + } ( \mathit { E b l o c k } ( \tilde { \mathit { P o w } } _ { t - 1 } ^ { T } ) )$ for $0 < t \leq h ;$ and $\mathit { P o w } _ { t } ^ { T } = \mathit { S i g } ^ { + } ( \mathit { E p o w } ( \mathit { S h r i n k } _ { t } ^ { T } ) )$ for $0 \leq t \leq h ;$ where h is the minimum integer satisfying $| P o w _ { h } ^ { T } | = 1$ **. Then, the start symbol of the** signature encoding is $S = P o w _ { h } ^ { T } .$ **We say that a node is in level t in the derivation tree of S if the node** is produced by $\tilde { { S h r i n k } _ { t } ^ { T } }$ or $P o w _ { t } ^ { T }$ . The height of the derivation tree of the signature encoding of $T$ **is** $O ( h ) = O ( \log | T | )$ **.** For any $T \in \Sigma ^ { + }$ , let $i d ( \tilde { T } ) = P o w _ { h } ^ { T } = S$ , i.e., the integer $S$ is the signature of T . **In this paper, we implement signature encodings by the** $\mathrm { D A G }$ **of** $\mathrm { R L S L P }$ **introduced in Section 2.**

## 4 Compressed LCE data structure using signature encodings

**In this section, we show Theorem 1.**

**Space requirement of the signature encoding. It is clear from the definition of the signature** encoding $\mathcal { G }$ of $T$ that the size of $\mathcal { G }$ is less than $4 N \leq M$ , and hence, all signatures are in $[ 1 . . M - 1 ]$ **Moreover, the next lemma shows that G requires only compressed space:**

**Lemma 6 ([23]). The size w of the signature encoding of T of length N is** $O ( z \log N \log ^ { * } M )$ **, where z** is the number of factors in the LZ77 factorization without self-reference $o f   T$

**Common sequences of signatures to all occurrences of same substrings. Here, we recall the most important property of the signature encoding, which ensures the existence of common signatures to all occurrences of same substrings by the following lemma.**

**Lemma 7 (common sequences [23]). Let G be a signature encoding for a string T . Every substring P in** $T$ **is represented by a signature sequence** $U n i q ( P )$ **in G for a string P.**

**Uniq(P), which we call the common sequence of** $P .$ **is defined by the following.**

**Definition 8. For a string P, let**

$$
\begin{array}{r c l l} X S h r i n k _ {t} ^ {P} & = & \left\{ \begin{array}{l l} S i g ^ {+} (P) & \text {for} t = 0, \\ S i g ^ {+} (E b l o c k _ {d} (X P o w _ {t - 1} ^ {P}) [ | L _ {t} ^ {P} |.. | X P o w _ {t - 1} ^ {P} | - | R _ {t} ^ {P} | ]) & \text {for} 0 <   t \leq h ^ {P}, \end{array} \right. \\ X P o w _ {t} ^ {P} & = & S i g ^ {+} (E p o w (X S h r i n k _ {t} ^ {P} [ | \hat {L} _ {t} ^ {P} | + 1.. | X S h r i n k _ {t} ^ {P} | - | \hat {R} _ {t} ^ {P} ]) |) & \text {for} 0 \leq t <   h ^ {P}, \text {where} \end{array}
$$

• $L _ { t } ^ { P }$ is the shortest prefix of $X P o w _ { t - 1 } ^ { P }$ of length at least $\Delta _ { L }$ such that $d [ | L _ { t } ^ { P } | + 1 ] = 1$

4

<!-- page 5 of 17 -->

• $R _ { t } ^ { P }$ is the shortest suffix of $X P o w _ { t - 1 } ^ { P }$ of length at least $\Delta _ { R } + 1$ such that $d [ | d | - | R _ { t } ^ { P } | + 1 ] = 1$

$\hat { L } _ { t } ^ { P }$ is the longest prefix of $X \mathit { S h r i n k } _ { t } ^ { P }$ such that $| \mathit { E p o w } ( \hat { L } _ { t } ^ { P } ) | = 1$

$\hat { R } _ { t } ^ { P }$ is the longest suffix of XShrink $c _ { t } ^ { P }$ such that $| \mathit { E p o w } ( \hat { R } _ { t } ^ { P } ) | = 1$ **, and**

• $h ^ { P }$ is the minimum integer such that $| \mathit { E p o w } ( \mathit { X S h r i n k } _ { h ^ { P } } ^ { P } ) | \leq \Delta _ { L } + \Delta _ { R } + 9 .$

Note that $\Delta _ { L }   \leq   | L _ { t } ^ { P } |   \leq   \Delta _ { L }   +   3$ and $\Delta _ { R }   +   1   \leq   | R _ { t } ^ { P } |   \leq   \Delta _ { R }   +   4$ **hold by the definition. Hence** $| \mathit { X S h r i n k } _ { t + 1 } ^ { P } | > 0$ holds if $| \mathit { E p o w } ( \mathit { X S h r i n k } _ { t } ^ { P } ) | > \Delta _ { L } + \Delta _ { R } + 9$ **. Then,**

$$
U n i q (P) = \hat {L} _ {0} ^ {P} L _ {0} ^ {P} \dots \hat {L} _ {h ^ {P} - 1} ^ {P} L _ {h ^ {P} - 1} ^ {P} X S h r i n k _ {h ^ {P}} ^ {P} R _ {h ^ {P} - 1} ^ {P} \hat {R} _ {h ^ {P} - 1} ^ {P} \dots R _ {0} ^ {P} \hat {R} _ {0} ^ {P}.
$$

**We give an intuitive description of Lemma 7. Recall the locally consistent parsing of Lemma 5. Each i-th bit of bit sequence d of Lemma 5 for a given string s is determined by** $s [ i   -   \Delta _ { L } . . i   +   \Delta _ { R } ]$ **. Hence, for two** positions $i , j$ such that $P = s [ i . . i   +   k   -   1 ] = s [ j . . j   +   k   -   1 ]$ for some $k , d [ i + \Delta _ { L } . . i + k - 1 - \Delta _ { R } ] = d [ j + \Delta _ { L } . . j +$ $\left[ k - 1 - \Delta _ { R } \right]$ **holds, namely, “internal” bit sequences of the same substring of s are equal. Since each level of the signature encoding uses the bit sequence, all occurrences of same substrings in a string share same** internal signature sequences, and this goes up level by level. $X \mathit { S h r i n k } _ { t } ^ { P }$ and $\tilde { X P o w _ { t } ^ { P } }$ **represent signature** sequences obtained from only internal signature sequences of $X P o w _ { t - 1 } ^ { T }$ and $X \bar { S h r i n k _ { t } ^ { T } }$ **, respectively. This** means that $\mathit { X S h r i n k } _ { t } ^ { P }$ and $X { P } o w _ { t } ^ { P }$ **are always created over P. From such common signatures we take as** short signature sequence as possible for $U n i q ( P )$ : Since v $\mathit { a l } ^ { + } ( \mathit { P o w } _ { t - 1 } ^ { P } ) = \mathit { v a l } ^ { + } ( L _ { t - 1 } ^ { P } \overset { \sim } { \mathit { X S h r i n k } } _ { t } ^ { P } R _ { t - 1 } ^ { P } )$ **and** $v a l ^ { + } ( \mathit { S h r i n k } _ { t } ^ { P } ) = v a l ^ { + } ( \hat { L } _ { t } ^ { P } \mathit { X P o w } _ { t } ^ { P } \hat { R } _ { t } ^ { P } )$ hold, |Epow $| ( { \it U n i q } ( P ) ) | = O ( \log | P | \log ^ { * } M )$ and v $\mathit { v a l } ^ { + } ( \mathit { U n i q } ( P ) ) =$ $P$ **hold. Hence Lemma 7 holds** <strong><sup>1</sup></strong>.

**The number of ancestors of nodes corresponding to Uniq(P) is upper bounded by:**

Lemma 9. Let $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ be a signature encoding for a string T , P be a string, and let $\mathcal { T }$ be $t h e$ derivation tree of a signature $e \in \mathcal { V }$ . Consider an occurrence of P in s, and the induced subtree X of $\mathcal { T }$ whose root is the root of T and whose leaves are the parents of the nodes representing $U n i q ( P )$ **, where** $s   =   v a l ( e )$ **. Then X contains** $O ( \log ^ { * } M )$ **n**odes for every level and $O ( \operatorname { l o g } | s | + \operatorname { l o g } | P | \operatorname { l o g } ^ { * } M )$ nodes in **total.**

**LCE queries. In the next lemma, we show a more general result than Theorem 1, which states that the signature encoding supports (both forward and backward) LCE queries on a given arbitrary pair of signatures. Theorem 1 immediately follows from Lemma 10.**

**Lemma 10. Using a signature encoding** $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **for a string** $T _ { \perp }$ **, we can support queries** $\mathsf { L C E } ( s _ { 1 } , s _ { 2 } , i , j )$ and $\mathsf { L C E } ( \bar { s _ { 1 } ^ { R } } , s _ { 2 } ^ { R } , i , j )$ **in** $O ( \operatorname { l o g } | s _ { 1 } | + \operatorname { l o g } | s _ { 2 } | + \operatorname { l o g } \ell \operatorname { l o g } ^ { * } M )$ **time for given two signatures** $e _ { 1 } , e _ { 2 } \in \mathcal { V }$ **and two integers** $1 \leq i \leq |s_1|, \quad 1 \leq j \leq |s_2|$ **, where** $s _ { 1 }   =   v a l ( e _ { 1 } ) , \; s _ { 2 }   =   v a l ( e _ { 2 } )$ **and ℓ is the answer to the** LCE **query.**

**Proof. We focus on** $\mathsf { L C E } ( s _ { 1 } , s _ { 2 } , i , j )$ as $\mathsf { L C E } ( s _ { 1 } ^ { R } , s _ { 2 } ^ { R } , i , j )$ **is supported similarly.**

**Let P denote the longest common prefix of** $s _ { 1 } [ i . . ]$ **and** $s _ { 2 } [ j . . ]$ **. Our algorithm simultaneously traverses two derivation trees rooted at** $e _ { 1 }$ **and** $e _ { 2 }$ **and computes** $P$ **by matching the common signatures greedily from left to right. Recall that** $s _ { 1 }$ **and** $s _ { 2 }$ **are substrings of** $T .$ **Since the both substrings** $P$ **occurring at position i in val** $( e _ { 1 } )$ **and at position** $j$ **in** $v a l ( e _ { 2 } )$ **are represented by** $\mathit { U n i q } ( P )$ **in the signature encoding by Lemma** $^ { 7 , }$ **we can compute** $P$ **by at least finding the common sequence of nodes which represents** $U n i q ( P )$ **, and hence, we only have to traverse ancestors of such nodes. By Lemma 9, the number of** nodes we traverse, which dominates the time complexity, is upper bounded by $O ( \operatorname { l o g } | s _ { 1 } | + \operatorname { l o g } | s _ { 2 } | +$ $\mathit { E p o w } ( \mathit { U n i q } ( P ) ) ) = O ( \operatorname { l o g } | s _ { 1 } | + \operatorname { l o g } | s _ { 2 } | + \operatorname { l o g } \ell \operatorname { l o g } ^ { * } \mathring { M } )$ □

## 5 Updates

**In this section, we show Theorem 2. Formally, we consider a dynamic signature encoding** $\mathcal { G }$ **of** $T$ **, which** allows for efficient updates of G in compressed space according to the following operations: $\mathit { I N S E R T } ( Y , i )$ inserts a string $Y$ into $T$ at position i, i.e., $T \leftarrow T [ . . i - 1 ] Y T [ i . . ] ;$ $\mathit { I N S E R T } ^ { \prime } ( j , y , i )$ inserts $T [ j . . j + y - 1 ]$ into $T$ **at position i, i.** $\operatorname { e . , } T \leftarrow T [ . . i - 1 ] T [ j . . j + \vec { y } - 1 ] \vec { T } [ i . . ] ;$ **and** $\mathit { D E L E T E } ( j , y )$ **deletes a substring of** length y starting at $j ,   \mathrm { i . e . , }   T \leftarrow T [ . . j - 1 ] T [ j + y . . ]$

<small><span class="docvortex-page-footnote" data-block-type="page_footnote" style="color:#6b7280">1 The common sequences are conceptually equivalent to the cores [17] which are defined for the edit sensitive parsing of a text, a kind of locally consistent parsing of the text.</span></small>

5

<!-- page 6 of 17 -->

During updates we recompute $S h r i n k _ { t } ^ { T }$ and $P o w _ { t } ^ { T }$ for some part of new $T$ **(note that the most part is unchanged thanks to the virtue of signature encodings, Lemma 9). When we need a signature for expr ,** we look up the signature assigned to expr (i.e., compute $\mathit { A s s i g n } ^ { - 1 } ( \mathit { e x p r } ) )$ **and use it if such exists. If** $\mathit { A s s i g n } ^ { - 1 } ( \mathit { e x p r } )$ **is undefined we create a new signature, which is an integer that is currently not used as signatures (say** $e _ { \mathit { n e w } } = \operatorname* { m i n } ( [ 1 . . M ] \setminus \mathcal { V } ) )$ **, and add** $e _ { n e w } \rightarrow$ **expr to D. Also, updates may produce a** useless signature whose parents in the DAG are all removed. We remove such useless signatures from $\mathcal { G }$ **during updates.**

**Note that the corresponding nodes and edges of the DAG can be added/removed in constant time per addition/removal of an assignment. In addition to the** $\mathrm { D A G } .$ **we need dynamic data structures to** conduct the following operations efficiently: (A) computing $A s s g n ^ { - 1 } ( \cdot )$ **,** (B) computing min $( [ 1 . . M ] \setminus \mathcal { V } )$ **and (C) checking if a signature e is useless.**

**For (A), we use Beame and Fich’s data structure [3] that can support predecessor/successor queries on a dynamic set of integers.**<strong><sup>2</sup></strong> **For example, we consider Beame and Fich’s data structure maintaining a** set of integers $\{ e _ { \ell } M ^ { 2 } + e _ { r } M + e \mid e \to e _ { \ell } e _ { r } \in \mathcal { D } \}$ in $O ( w )$ space. Then we can implement $A ^ {  } { s s g n } ^ { - 1 } \tilde { ( e _ { \ell } e _ { r } ) }$ by computing the successor q of $e _ { \ell } M ^ { 2 } + e _ { r } M ,   \mathrm { i . e . , } e = q$ **mod M if** $\lfloor q / M \rfloor = e _ { \ell } M + e _ { r } ,$ **and otherwise** $\mathring { A } { s s g n } ^ { - 1 } ( e _ { \ell } e _ { r } )$ is undefined. Queries as well as update operations can be done in deterministic $O ( f _ { \mathcal { A } } )$ time, where $\textstyle f _ { \mathcal { A } } = O \left( \operatorname* { m i n } \left\{ \frac { \operatorname { l o g } \operatorname { l o g } M \operatorname { l o g } \operatorname { l o g } w } { \operatorname { l o g } \operatorname { l o g } \operatorname { l o g } M } , \sqrt { \frac { \operatorname { l o g } w } { \operatorname { l o g } \operatorname { l o g } w } } \right\} \right)$

**For (B), we again use Beame and Fich’s data structure to maintain the set of maximal intervals such that every element in the intervals is signature. Formally, the intervals are maintained by a set of integers** $\{ e _ { i } M + e _ { j } \mid [ e _ { i } . . e _ { j } ] \subseteq \mathcal { V } , e _ { i } - 1 \notin \mathcal { V } , e _ { j } + 1 \notin \mathcal { V } \}$ **i**n $O ( w )$ **space. Then we can know the minimum integer currently not in** $\mathcal { V }$ **by computing the successor of 0.**

**For (C), we let every signature** $e \in \mathcal { V }$ **have a counter to count the number of parents of e in the DAG. Then we can know that a signature is useless if the counter is 0.**

**Lemma 11 shows that we can efficiently compute** $U n i q ( P )$ **for a substring P of** $T .$ **.**

**Lemma 11. Using a signature encoding** $\mathcal { G }   =   ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **of size w, given a signature** $e \in \mathcal { V }$ **(and its** corresponding node in the $D A G )$ and two integers j and $y ,$ we can compute $\mathit { E p o w } ( \mathit { U n i q } ( s [ j . . j + y - 1 ] ) )$ in $O ( \operatorname { l o g } | s | + \operatorname { l o g } y \operatorname { l o g } ^ { * } M )$ time, where $s = v a l ( e )$

**Proof of Theorem 2. It is easy to see that, given the static signature encoding of** $T ,$ **we can construct data structures** $( \mathrm{A} ) \text{-} ( \mathrm{C} )$ **in** $O ( w f _ { A } )$ **time.** After constructing these, we can add/remove an assignment in $O ( f _ { \mathcal { A } } )$ time.

Let $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ be the signature encoding before the update operation. We support $\mathit { D E L E T E } ( j , y )$ as follows: (1) Compute the new start variable $S ^ { \prime }   =   i d ( T [ . . j - 1 ] T [ j + y . . ] )$ **by recomputing the new signature encoding from** $\mathit { U n i q } ( T [ . . j   -   1 ] )$ **and** $U n i q ( T [ j   +   y . . ] )$ **. Although we need a part of d to recompute** $\tilde { \mathit { E b l o c k } } _ { d } ( \mathit { P o w } _ { t } ^ { T [ . . j - 1 ] T [ j + y . . ] } )$ for every level $t ,$ the input size to compute the part of $d$ **is** $O ( \log ^ { * } M )$ **by Lemma** $5 .$ **Hence these can be done in** $O ( f _ { \mathcal { A } } \operatorname { l o g } N \operatorname { l o g } ^ { * } M )$ **time by Lemmas 11 and 9. (2) Remove all useless signatures** $Z$ **from** $\mathcal { G } .$ **Note that if a signature is useless, then all the signatures along the path from S to it are also useless. Hence, we can remove all useless signatures efficiently by depth-first search starting from S, which takes** $O ( f _ { \mathcal { A } } | Z | )$ **time, where** $| Z | = O ( y + \log N \log ^ { * } M )$ **by Lemma 9.**

**Similarly, we can support** $\mathit { I N S E R T } ( Y , i )$ **in** $O ( f _ { \mathcal { A } } ( y +$ **log** $N \log ^ { * } M ) )$ **t**ime by creating the new start variable $S ^ { \prime }$ from $\mathit { U n i q } ( T [ . . i - 1 ] ) , \; \mathit { U n i q } ( Y )$ and $\mathit { U n i q } ( T [ i . . ] )$ . Note that we can naively compute $U n i q ( Y )$ in $O ( f _ { \mathcal { A } } y )$ time. For $\mathit { I N S E R T } ^ { \prime } ( j , y , i )$ , we can avoid $O ( f _ { \mathcal { A } } y )$ time by computing $\mathit { U n i q } ( T [ j . . j + y - 1 ] )$ **using Lemma 11.** 口

## 6 Construction

**In this section, we give proofs of Theorem 3, but we omit proofs of the results (2) and (3a) as they are straightforward from the previous work [2, 1].**

## 6.1 Theorem 3 (1a)

**Proof of Theorem 3** $( I a )$ **. Note that we can naively compute** $i d ( T )$ **for a given string** $T$ **in** $O ( N f _ { \mathcal { A } } )$ **time and** $O ( N )$ **working space. In order to reduce the working space, we consider factorizing** $T$ **into blocks of size B and processing them incrementally: Starting with the empty signature encoding** $\mathcal { G } ,$ **we can**

<small><span class="docvortex-page-footnote" data-block-type="page_footnote" style="color:#6b7280">2 Alstrup et al. [1] used hashing for this purpose. However, since we are interested in the worst case time complexities, we use the data structure [3] in place of hashing.</span></small>

6

<!-- page 7 of 17 -->

compute id(T ) in $\textstyle O ( \frac { N } { B } f _ { \mathcal { A } } ( \operatorname { l o g } N \operatorname { l o g } ^ { * } M + B ) )$ time and $O ( w   +   B )$ working space by using $\mathit { I N S E R T } ( T [ ( i -$ $1 ) B + 1 . . i B ] , ( i - 1 ) B + 1 )$ for $\begin{array} { r } { i = 1 , \ldots , \frac { N } { B } } \end{array}$ **in increasing order. Hence our proof is finished by choosing** $B = \log N \log ^ { * } M$ □

## 6.2 Theorem 3 (1b)

We compute signatures level by level, i.e., construct $\mathit { S h r i n k } _ { 0 } ^ { T } , \mathit { P o w } _ { 0 } ^ { T } , \ldots , \mathit { S h r i n k } _ { h } ^ { T } , \mathit { P o w } _ { h } ^ { T }$ incrementally. For each level, we create signatures by sorting signature blocks (or run-length encoded signatures) to **which we give signatures, as shown by the next two lemmas.**

Lemma 12. Given $\mathit { E b l o c k } ( \mathit { P o w } _ { t - 1 } ^ { T } ) \; \mathit { f o r } \; 0 < t \leq h _ { \iota }$ **, we can compute Shrink T**t in $O ( ( b - a ) + | P o w _ { t - 1 } ^ { T } | )$ time and space, where b is the maximum integer in $P o w _ { t - 1 } ^ { T }$ and a is the minimum integer in $P o w _ { t - 1 } ^ { T } .$

**Proof. Since we assign signatures to signature blocks and run-length signatures in the derivation tree** of $S$ in the order they appear in the signature encoding. $\bar { { P o w } _ { t - 1 } ^ { T } [ i ] - a }$ fits in an entry of a bucket of size $b - a$ for each element of $P o w _ { t - 1 } ^ { T } [ i ]$ of $P o w _ { t - 1 } ^ { T }$ . Also, the length of each block is at most four. Hence we can sort all the blocks of Eblock $( P o w _ { t - 1 } ^ { T } )$ by bucket sort in $O ( ( b - a ) + | P o w _ { t - 1 } ^ { T } | )$ time and space. Since Sig is an injection and since we process the levels in increasing order, for any two different levels $0 \leq t ^ { \prime } < t \leq h .$ , no elements of $S h r i n k _ { t - 1 } ^ { \tilde { T } ^ { \tilde { } } }$ appear in $\mathit { S h r i n k } _ { t ^ { \prime } - 1 } ^ { T }$ , and hence no elements **of** $P o w _ { t - 1 } ^ { T }$ **appear in** $P o w _ { t ^ { \prime } - 1 } ^ { T } .$ **T**hus, we can determine a new signature for each block in $\mathit { E b l o c k } ( \mathit { P o w } _ { t - 1 } ^ { T } )$ **, without searching existing signatures in the lower levels. This completes the proof.** □

Lemma 13. Given $\mathit { E p o w } ( \mathit { S h r i n k } _ { t } ^ { T } )$ , we can compute $P o w _ { t } ^ { T }$ in $O ( x + ( b - a ) + | \mathit { E p o w } ( \mathit { S h r i n k } _ { t } ^ { T } )$ |) time and space, where x is the maximum length of runs in $\dot { \mathit { E p o w } } ( \dot { \mathit { S h r i n k } } _ { t } ^ { T } )$ **, b is the maximum integer** in ${ P o w } _ { t - 1 } ^ { T } ,$ , and a is the minimum integer in $P o w _ { t - 1 } ^ { T }$

Proof. We first sort all the elements of $\mathit { E p o w } ( \mathit { S h r i n k } _ { t } ^ { T } )$ by bucket sort in $O(b - a +$ $| \dot { \mathit { E p o w } } ( \mathit { S h r i n k } _ { t } ^ { T } ) | )$ **time and space, ignoring the powers of runs. Then, for each integer r appearing** in $\dot { { S h r i n k } _ { t } ^ { T } }$ **, we sort the runs of** $r ^ { \prime } \mathrm { { s } }$ **by bucket sort with a bucket of size x. This takes a total of** $O ( x + | \overset { \circ } { \mathit { E p o w } } ( \mathit { S h r i n k } _ { t } ^ { T } ) | )$ time and space for all integers appearing in $S h r i n k _ { t } ^ { T }$ **. The rest is the same as the proof of Lemma 12.** □

**Proof of Theorem** $\mathcal { B } ( I b )$ **. Since the size of the derivation tree of** $i d ( T )$ **is** $O ( N )$ **,** by Lemmas 5, 12, and 13, **we can compute a DAG of G for T in** $O ( N )$ **time and space.** □

## 6.3 Theorem 3 (3b)

**In this section, we sometimes abbreviate** $v a l ( X )$ **as X for** $X \in { \mathcal { S } }$ **. For example, Shrink**<strong><sup>X</sup></strong>t and $P o w _ { t } ^ { X }$ represents $\mathit { S h r i n k } _ { t } ^ { \mathit { v a l } ( X ) }$ and ${ P o w } _ { t } ^ { { v a l } ( X ) }$ respectively.

Our algorithm computes signatures level by level, i.e., constructs incrementally $S h r i n k _ { 0 } ^ { X _ { n } }$

$\hat { \mathit { P o w } } _ { 0 } ^ { X _ { n } } , \: \hat { \ldots } , \hat { \mathit { S h r i n k } } _ { h } ^ { X _ { n } } , \hat { \mathit { P o w } } _ { h } ^ { X _ { n } }$ . Like the algorithm described in Section 6.2, we can create signatures by **sorting blocks of signatures or run-length encoded signatures in the same level. The main difference is** that we now utilize the structure of the SLP, which allows us to do the task efficiently in $O ( n \log ^ { * } M +$ w) working space. In particular, although $\left[ \mathit { S h r i n k } _ { t } ^ { X _ { n } } \right] , \left| \mathit { P o w } _ { t } ^ { X _ { n } } \right| \: = \: O ( N )$ **for** $0   \leq   t   \leq   h$ **, they can be represented in** $O ( n \log ^ { * } M )$ **space.**

In so doing, we introduce some additional notations relating to $X \mathit { S h r i n k } _ { t } ^ { P }$ and $X P o w _ { t } ^ { P }$ **in Definition 8.** $\mathrm { B y }$ Lemma 7, there exist $\hat { z } _ { t } ^ { ( P _ { 1 } , P _ { 2 } ) }$ and $z _ { t } ^ { ( P _ { 1 } , P _ { 2 } ) }$ for any string $P = P _ { 1 } P _ { 2 }$ **such that the following equation** holds: XShrink $k _ { t } ^ { P } = \hat { y } _ { t } ^ { P _ { 1 } } \hat { z } _ { t } ^ { ( \hat { P _ { 1 } } , P _ { 2 } ) } \hat { y } _ { t } ^ { P _ { 2 } }$ for $0 < t \leq h ^ { P }$ , and XPow $\begin{array} { r } { \iota _ { t } ^ { P } = y _ { t } ^ { P _ { 1 } } z _ { t } ^ { ( P _ { 1 } , P _ { 2 } ) } y _ { t } ^ { P _ { 2 } } } \end{array}$ for $0 \leq t < h ^ { P }$ **, where** we define $\hat { y } _ { t } ^ { P }$ and $y _ { t } ^ { P }$ for a string P as:

$$
\hat {y} _ {t} ^ {P} = \left\{ \begin{array}{l l} X S h r i n k _ {t} ^ {P} & \text {for} 0 <   t \leq h ^ {P}, \\ \varepsilon & \text {for} t > h ^ {P}, \end{array} \right. y _ {t} ^ {P} = \left\{ \begin{array}{l l} X P o w _ {t} ^ {P} & \text {for} 0 \leq t <   h ^ {P}, \\ \varepsilon & \text {for} t \geq h ^ {P}. \end{array} \right.
$$

For any variable $X _ { i } \: \to \: X _ { \ell } X _ { r } ,$ we denote $\hat { z } _ { t } ^ { X _ { i } } \: = \: \hat { z } _ { t } ^ { ( v a l ( X _ { \ell } ) , v a l ( X _ { r } ) ) }$ (for $0 \: < \: t \: \leq \: h ^ { v a l ( X _ { i } ) } )$ and $z _ { t } ^ { X _ { i } } =$ $z _ { \ell } ^ { ( \stackrel { { v a l } ( X _ { \ell } ) } { } , \stackrel { { v } } { } { v a l } ( X _ { r } ) ) }$ (for $0   \leq   t   <   h ^ { v a l ( X _ { i } ) } )$ . Note that $| z _ { t } ^ { X _ { i } } | , | \hat { z } _ { t } ^ { X _ { i } } | = O ( \operatorname { l o g } ^ { * } M )$ because $z _ { t } ^ { X _ { i } }$ **i**s created on $\widetilde { \hat { R } } _ { t } ^ { \hat { { X } } _ { \ell } } \hat { z } _ { t } ^ { { X } _ { i } } \hat { L } _ { t } ^ { { X } _ { r } }$ , similarly, $\hat { z } _ { t } ^ { X _ { i } }$ **i**s created on $R _ { t - 1 } ^ { X _ { \ell } } z _ { t - 1 } ^ { X _ { i } } L _ { t - 1 } ^ { \overleftarrow { X _ { r } } }$ . We can use $\hat { z } _ { t } ^ { \widetilde { X } _ { 1 } } , \dots , \hat { z } _ { t } ^ { X _ { n } }$ (resp. $z _ { t } ^ { X _ { 1 } } , \ldots , z _ { t } ^ { X _ { n } } )$ as a compressed representation of $\mathit { X S h r i n k } _ { t } ^ { X _ { n } } ~ ( \operatorname { r e s p . } ~ \mathit { X P o w } _ { t _ { n } } ^ { X _ { n } } )$ based on the ${ \mathrm { S L P } } ;$ Intuitively, $\hat { z } _ { t } ^ { X _ { n } } ( \operatorname { r e s p } .$ $z _ { t } ^ { X _ { n } } )$ covers the middle part of $\mathit { X S h r i n k } _ { t } ^ { \mathit { X } _ { n } ^ { \prime } } \quad ( \mathrm { r e s p . } \quad \mathit { X P o w } _ { t } ^ { \mathit { X } _ { n } } )$ and the remaining part is recovered by

7

<!-- page 8 of 17 -->

![Image block](doc:d41ff6f/tier:standard/page:8/block:1)

Figure 1: XPowXnt can be represented by $z _ { t } ^ { X _ { 1 } } , \ldots , z _ { t } ^ { X _ { n } }$ . In this example, $\begin{array} { r l } { \mathit { X P o w } _ { t } ^ { X _ { n } } } & { { } = } \end{array}$ $z _ { t } ^ { \stackrel { \smile } { X _ { n - 5 } } } z _ { t } ^ { X _ { n - 3 } } z _ { t } ^ { X _ { n - 6 } } z _ { t } ^ { \stackrel { \triangledown } { X _ { n - 1 } } } z _ { t } ^ { X _ { n - 4 } } z _ { t } ^ { X _ { n } } z _ { t } ^ { \stackrel { \triangledown } { X _ { n - 7 } } } z _ { t } ^ { X _ { n - 2 } }$

![Image block](doc:d41ff6f/tier:standard/page:8/block:3)

Figure 2: An abstract image of $S h r i n k _ { t } ^ { P }$ and $P o w _ { t } ^ { P }$ for a string P. For $0   \leq   t   <   h ^ { P } , \; A _ { t } ^ { P } L _ { t } ^ { P }$ (resp. $R _ { t } ^ { \widetilde { P } } B _ { t } ^ { P } )$ is encoded into $\left| \hat { A } _ { t + 1 } ^ { P } \right|$ (resp. $\hat { B } _ { t + 1 } ^ { P } )$ . Similarly, for $0 < t < \stackrel { \circ } { h ^ { P } } ,   \hat { A } _ { t } ^ { P } \hat { L } _ { t } ^ { P }$ (resp. $\hat { R } _ { t } ^ { P } \hat { B } _ { t } ^ { P } )$ is encoded into $\stackrel{\circ}{A_{t}^{P}} (\mathrm{resp.} B_{t}^{P})$

**investigating the left/right child recursively (see also Fig. 1). Hence, with the DAG structure of the SLP,** $\overset { \circ } { \underset {} { X } { { S h r i n k } _ { t } ^ { X _ { n } } } }$ and $X \acute { P o w _ { t } ^ { \tilde { X } _ { n } } }$ can be represented in $O ( n \operatorname { l o g } ^ { * } \dot { M } )$ **space.**

In addition, we define $\hat { A } _ { t } ^ { P } ,   \hat { B } _ { t } ^ { P } , \hat { A } _ { t } ^ { P }$ and $B _ { t } ^ { P }$ as follows: For $0 < t \leq h ^ { P } ,   \hat { A } _ { t } ^ { P }$ (resp. $\hat { B } _ { t } ^ { P } )$ is a prefix (resp. suffix) of $S h r i n k _ { t } ^ { P }$ which consists of signatures of $A_{t - 1}^{P}L_{t - 1}^{P} \; ( \mathrm{resp.} \; R_{t - 1}^{P}\tilde{B_{t - 1}^{P}} \dot{)};$ and for $\bar { [ 0 \leq t < h ^ { P } }$ $A _ { t } ^ { P } \mathrm { ~ ( r e s p . ~ } B _ { t } ^ { P } \mathrm { ) }$ is a prefix (resp. suffix) of $P o w _ { t } ^ { P }$ which consists of signatures of $\hat { A } _ { t } ^ { P } \hat { L } _ { t } ^ { P }$ (resp. $\hat { R } _ { t } ^ { P } \hat { B } _ { t } ^ { P } )$ . By the definition, $\hat { \mathit { S h r i n k } } _ { t } ^ { P } = \hat { \mathit { A } } _ { t } ^ { P } \hat { \mathit { X } } \hat { \mathit { S h r i n k } } _ { t } ^ { P } \hat { \mathit { B } } _ { t } ^ { P }$ for $0 \leq t \leq h ^ { P }$ , and Pow $v _ { t } ^ { P } = A _ { t } ^ { P } \overset { v } { \underset {} { X } { \mathop { { P o w } } } } _ { t } ^ { \overset { v } { \mathop { { P } } } } \overset { \varsigma } { B _ { t } ^ { P } }$ for $0 \leq \dot { t } < h ^ { \vec { P } }$ See Fig. 2 for the illustration.

$$
S h r i n k _ {t} ^ {X _ {n}} = \hat {A} _ {t} ^ {X _ {n}} X S h r i n k _ {t} ^ {X _ {n}} \hat {B} _ {t} ^ {X _ {n}}
$$

$$
0 <   t <   h ^ {X _ {n}}
$$

$$
\hat {\Lambda} _ {t} = (\hat {z} _ {t} ^ {X _ {1}}, \dots , \hat {z} _ {t} ^ {X _ {n}}, \hat {A} _ {t} ^ {X _ {n}})
$$

$\hat { B } _ { t } ^ { X _ { n } } )$ as a compressed representation of $\mathit { S h r i n k } _ { t } ^ { X _ { n } }$ <sub>of</sub> size $O ( n \log ^ { * } M )$ **. Similarly, for** $0 \stackrel { \circ } { \leq } t < h ^ { X _ { n } }$ **, we** use $\hat { \Lambda } _ { t } = ( z _ { t } ^ { X _ { 1 } } , \dots , z _ { t } ^ { X _ { n } } , \bar { A _ { t } ^ { X _ { n } } } , B _ { t } ^ { X _ { n } } )$ as a compressed representation of $\left[ { P o w } _ { t } ^ { X _ { n } } \right.$ of size $O ( n \log ^ { * } M )$

Our algorithm computes incrementally $\Lambda _ { 0 } , \hat { \Lambda } _ { 1 } , \ldots , \hat { \Lambda } _ { h ^ { X _ { n } } }$ . Given $\hat { \Lambda } _ { h ^ { X _ { n } } }$ , we can easily get $\dot { { P } o w _ { h ^ { X _ { n } } } ^ { X _ { n } } }$ **of** size $O ( \log ^ { * } M )$ **in** $O ( n \log ^ { * } M )$ time, and then $i d ( v a l ( X _ { n } ) )$ in $O ( \log ^ { * } M )$ time from $P o w _ { h ^ { X _ { n } } } ^ { X _ { n } }$ **.** Hence, in the following three lemmas, we show how to compute $\Lambda _ { 0 } , \hat { \Lambda } _ { 1 } , \ldots , \hat { \Lambda } _ { h ^ { X _ { n } } }$

**Lemma 14. Given an SLP of size n, we can compute** $\Lambda _ { 0 }$ **in** $O ( n \operatorname { l o g } \operatorname { l o g } ( n \operatorname { l o g } ^ { * } M ) \operatorname { l o g } ^ { * } M )$ **time and** $O ( n \log ^ { * } M )$ **space.**

Proof. We first compute, for all variables $X _ { i } , \; \mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { i } } ) \; \mathit { 1 f } \; | \mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { i } } ) | \leq \Delta _ { L } + \Delta _ { R } + 9 ,$ otherwise $\mathit { E p o w } ( \hat { L } _ { 0 } ^ { \hat { X _ { i } } } )$ and $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { i } } )$ The information can be computed in $O ( n \log ^ { * } M )$ time and space in a bottom-up manner, i.e., by processing variables in increasing order. For $X _ { i } \: \to \: X _ { \ell } X _ { r } ,$ **i**f both $| \mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { \bar { \mathit { X } } _ { \ell } } ) |$ and $| \mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { r } } ) |$ are no greater than $\Delta _ { L } + \Delta _ { R } + 9$ , we can compute $\mathit { E p o w } ( \widehat { \mathit { X S h r i n k } } _ { 0 } ^ { X _ { i } } )$ **i**n $\stackrel{\circ}{O} (\log^* M)$ time by naively concatenating $\mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { \ell } } )$ **and** $\acute { \mathit { E p o w } } ( \mathit { X S h r i n k } _ { 0 } ^ { \acute { \mathit { X } } _ { r } } )$ Otherwise |Epow $| ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { i } } ) | \; > \; \Delta _ { L } \: + \: \Delta _ { R } \: + \: 9$ must hold, and $\mathit { E p o w } ( \hat { L } _ { 0 } ^ { \hat { X _ { i } } } )$ and $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { i } } )$ **can be** computed in $O ( 1 )$ time from the information for $X _ { \ell }$ and $X _ { r }$

The run-length encoded signatures represented by $z _ { 0 } ^ { X _ { i } }$ can be obtained by using the above information for $X _ { \ell }$ and $X _ { r }$ **i**n $O ( \log ^ { * } M )$ **time:** $z _ { 0 } ^ { X _ { i } }$ **i**s created over run-length encoded signatures $\mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { \ell } } )$ (or $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { \ell } } ) )$ followed by Epow $\mathring { ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { r } } ) }$ (or $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { \widetilde { X } _ { r } } ) )$ . Also, by definition $A _ { 0 } ^ { \dot { X _ { n } } }$ and $\vec { B _ { 0 } ^ { X _ { n } } }$ **r**epresents Epow $( \hat { L } _ { 0 } ^ { X _ { n } } )$ and $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { n } } )$ **, respectively.**

**Hence, we can compute in** $O ( n \log ^ { * } M )$ **time** $O ( n \log ^ { * } M )$ **run-length encoded signatures to which we give signatures. We determine signatures by sorting the run-length encoded signatures as Lemma 13.**

8

<!-- page 9 of 17 -->

![Image block](doc:d41ff6f/tier:standard/page:9/block:1)

Figure 3: Abstract images of the needed signature sequence $v _ { t } ^ { X _ { \ell } } z _ { t } ^ { X _ { i } } u _ { t } ^ { X _ { r } } ( v _ { t } ^ { X _ { \ell } }$ and $u _ { t } ^ { X _ { r } }$ are not shown when they are empty) for computing $\hat { z } _ { t + 1 } ^ { X _ { i } }$ in three situations: Top for $0   \leq   t   <   h ^ { X _ { \ell } } , h ^ { X _ { r } }$ ; middle for $h ^ { X _ { r } } \leq t \stackrel { \circ } { < } h ^ { X _ { \ell } } ;$ and bottom for $\left[ h ^ { X _ { \ell } } , \overset { \sim } { h } \overset { v + 1 } { \underset {} { X _ { r } } { \leq } } t < h ^ { X _ { i } } \right.$

**However, in contrast to Lemma 13, we do not use bucket sort for sorting the powers of runs because the maximum length of runs could be as large as N and we cannot afford** $O ( N )$ **space for buckets. Instead, we use the sorting algorithm of Han [12] which sorts x integers in O(x log log x) time and** $O ( x )$ **space. Hence, we can compute** $\Lambda _ { 0 }$ **in** $O ( n \widetilde { \operatorname { l o g } \operatorname { l o g } } ( n \operatorname { l o g } ^ { * } M ) \operatorname { l o g } ^ { * } M )$ **time and** $\overset { \cdot } { O } ( n \operatorname { l o g } ^ { * } \overset { \cdot } { M } )$ **space.** □

Lemma 15. Given $\hat { \Lambda } _ { t } ,$ **we can compute** $\Lambda _ { t }$ **in** $O ( n \operatorname { l o g } \operatorname { l o g } ( n \operatorname { l o g } ^ { * } M ) \operatorname { l o g } ^ { * } M )$ **time and** $O ( n \log ^ { * } M )$ **space.**

Proof. The computation is similar to that of Lemma 14 except that we also use $\hat { \Lambda } _ { t }$

Lemma 16. Given $\Lambda _ { t }$ , we can compute $\hat { \Lambda } _ { t + 1 }$ **in O(n log∗ M) time and** $O ( n \log ^ { * } M )$ **space.**

Proof. In order to compute $\hat { z } _ { t + 1 } ^ { X _ { i } }$ for a variable $X _ { i } \to X _ { \ell } X _ { r } .$ we need a signature sequence on which $\hat { z } _ { t + 1 } ^ { X _ { i } }$ **i**s created, as well as its context, $\mathrm { i . e . , } ~ \Delta _ { L }$ **signatures to the left and** $\Delta _ { R }$ **to the right. To be precise, the** needed signature sequence is $v _ { t } ^ { X _ { \ell } } z _ { t } ^ { X _ { i } } u _ { t } ^ { X _ { r } }$ , where $u_{t}^{X_{j}} (\mathrm{resp.} v_{t}^{X_{j}})$ denotes a prefix (resp. suffix) of $y _ { t } ^ { \dot { X } _ { j } }$ of length $\Delta _ { L } + \Delta _ { R } + 4$ for any variable $X _ { j }$ (see also Figure 3). Also, we need $A _ { t } u _ { t } ^ { X _ { n } ^ { \urcorner } }$ and $v _ { t } ^ { \vec { X } _ { n } } \vec { B _ { t } }$ **t**o create $\hat { A } _ { t + 1 } ^ { X _ { n } }$ and $\hat { B } _ { t + 1 } ^ { X _ { n } }$ **, respectively.**

Note that by Definition $8, \left| z_{t}^{X} \right| > \Delta_{L} + \Delta_{R} + 9   if   z_{t}^{X} \neq \varepsilon$ . Then, we can compute $u _ { t } ^ { X _ { i } }$ for all variables $X _ { i }$ **i**n O(n log∗ M) time and space by processing variables in increasing order on the basis of the following fact: $u _ { t } ^ { \vec { X _ { i } } } = \overset { \circ } { u _ { t } ^ { X _ { \ell } } } \mathrm { ~ i f ~ } z _ { t } ^ { X _ { \ell } } \neq \varepsilon ,$ , otherwise $u _ { t } ^ { X _ { i } }$ is the prefix of $z _ { t } ^ { X _ { i } }$ of length $\Delta _ { L } + \Delta _ { R } + 4$ . Similarly $v _ { t } ^ { X _ { i } }$ **for all variables** $X _ { i }$ can be computed in O(n log M) time and space.

Using $u _ { t } ^ { X _ { i } }$ and $v _ { t } ^ { X _ { i } }$ for all variables $X _ { i } .$ we can obtain $O ( n \log ^ { * } M )$ blocks of signatures to which we give signatures. We determine signatures by sorting the blocks by bucket sort as in Lemma 12 in $O ( n \log ^ { * } M )$ **t**ime. Hence, we can get $\hat { \Lambda } _ { t + 1 }$ **i**n $O ( n \log ^ { * } M )$ **t**ime and space. □

Proof of Theorem $\mathcal { B } ( \mathcal { B } b )$ . Using Lemmas 14, 15 and 16, we can get $\hat { \Lambda } _ { h ^ { X _ { n } } }$ **in O(n log log**

(n log∗ M) log N log∗ M) time by computing $\Lambda _ { 0 } , \hat { \Lambda } _ { 1 } , \ldots , \hat { \Lambda } _ { h ^ { X _ { n } } }$ incrementally. Note that during the computation we only have to keep $\Lambda _ { t } ( \operatorname { o r } \hat { \Lambda } _ { t } )$ **for the current t and the assignments of G. Hence the working** space is $O ( n \log ^ { * } M + w )$ . By processing $\hat { \Lambda } _ { h ^ { X _ { r } } }$ **in** $O ( n \log ^ { * } M )$ **time, we can get the DAG of G of size** $O ( w )$ □

## 7 Applications

**Theorem 17 is an application to text compression. Theorems 19-23 are applications to compressed string processing, where the task is to process a given compressed representation of string(s) without explicit decompression. We believe that only a few applications are listed here, considering the importance of LCE queries. As one example of unlisted applications, there is a paper [14] in which our LCE data structure was used to improve an algorithm of computing the Lyndon factorization of a string represented by a given SLP.**

**Theorem 17. (1) Given a dynamic signature encoding G for** $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **of size w which generates** $T ,$ **we can compute an SLP S of size** $O ( w \log | T | )$ **generating T in O(w log |T |) time. (2) Let us conduct a single INSERT or DELETE operation on the string T generated by the SLP of (1). Let y be the length**

9

<!-- page 10 of 17 -->

of the substring to be inserted or deleted, and let $T ^ { \prime }$ **be the resulting string. During the above operation** on the string, we can update, in $O ( ( y + \operatorname { l o g } | T ^ { \prime } | \operatorname { l o g } ^ { * } M ) ( f _ { \mathcal { A } } + \operatorname { l o g } | T ^ { \prime } | ) )$ time, the $\mathit { S L P ~ o f ~ ( 1 ) }$ to an $S L P$ $S ^ { \prime }$ of size $O ( w ^ { \prime } \log | T ^ { \prime } | )$ which generates $T ^ { \prime }$ , where $w ^ { \prime }$ is the size of updated $\mathcal { G }$ which generates $T ^ { \prime }$

**We can get the next lemma using Theorem 3 (3b) and Theorem 2:**

**Lemma 18. Given an** $S L P$ **of size n representing a string of length** $N ,$ **we can sort the variables of the** $S L P$ **in lexicographical order in O(n log n log** $N \log ^ { * } N )$ **time and** $O ( n \log ^ { * } N + w )$ **working space.**

**Lemma 18 has an application to an SLP-based index of Claude and Navarro [8]. In the paper, they showed how to construct their index in O(n log n) time if the lexicographic order of variables of a given SLP is already computed. However, in order to sort variables they almost decompressed the string, and hence, needs** $\Omega ( N )$ **time and** $\Omega ( N \log | \Sigma | )$ **bits of working space. Now, Lemma 18 improves the sorting part yielding the next theorem.**

**Theorem 19. Given an SLP of size n representing a string of length** $N ,$ **we can construct the SLP-based index of [8] in O(n log n log** $N \log ^ { * } N )$ **time and** $O ( n \log ^ { * } N + w )$ **working space.**

**Theorem 20. Given an** $S L P ~ { \mathcal { S } }$ **of size n generating a string T of length** $N ,$ **we can construct, in O(n log log n log** $N \log ^ { * } N )$ **time, a data structure which occupies O(n log** $N \log ^ { * } N )$ **s**pace and supports $\mathsf { L C P } ( { v a l } ( X _ { i } ) , { v a l } ( X _ { j } ) )$ **and** $\mathsf { L C S } ( { v a l } ( X _ { i } ) , { v a l } ( X _ { j } ) )$ **queries for variables** $X _ { i } , X _ { j }$ **i**n $O ( \log N )$ **time. The** $\mathsf { L C P } ( { v a l } ( X _ { i } ) , { v a l } ( \vec { X _ { j } } ) )$ and $\mathsf { L C S } ( { v a l } ( X _ { i } ) , { v a l } ( X _ { j } ) )$ query times can be improved to $O ( 1 )$ using $O ( n$ log n log $N \log ^ { * } N )$ **preprocessing time.**

**Theorem 21. Given an** $S L P \; { \mathcal { S } }$ **of size n generating a string T of length** $N _ { z }$ **there is a data structure** which occupies $O ( w + n )$ space and supports queries $\mathsf { L C E } ( { v a l } ( X _ { i } ) , { v a l } ( X _ { j } ) , a , b )$ for variables $X _ { i } , X _ { j } ,$ $1   \leq   a   \leq   \left| X _ { i } \right|$ **and** $1   \leq   b   \leq   | X _ { j } |$ **i**n $O ( \operatorname { l o g } N + \operatorname { l o g } \ell \operatorname { l o g } ^ { * } N )$ time, where $\stackrel { \cdot } { w }   =   O ( z \log N \log ^ { * } N )$ . The data structure can be constructed in O(n log log n log $N \log ^ { * } N )$ preprocessing time and $O ( n \log ^ { * } N + w )$ working space, where $z \leq n$ **i**s the size of the LZ77 factorization $o f   T$ **and ℓ is the answer of LCE query.**

**Let h be the height of the derivation tree of a given SLP S. Note that** $h   \geq   \log N$ **. Matsubara et al. [18] showed an** $O ( n h ( n + h \log N ) ) { \mathrm { - t i m e ~ } } O ( n ( n + \log N ) )$ **-**space algorithm to compute an $O ( n \log N ) \cdotp$ **size representation of all palindromes in the string. Their algorithm uses a data structure which supports** in $O ( h ^ { 2 } )$ **time,** LCE **queries of a special form** LCE**(val(X**<strong><sub>i</sub></strong>**), val(X**<strong><sub>j</sub></strong> **), 1, pj) [20]. This data structure takes** $O ( n ^ { 2 } )$ space and can be constructed in $O ( n ^ { 2 } h )$ **time [16]. Using Theorem 21, we obtain a faster algorithm, as follows:**

Theorem 22. Given an $S L P$ of size n generating a string of length N, we can compute an $O ( n \log N )$ size representation of all palindromes in the string in $O ( n \operatorname { l o g } ^ { 2 } N \operatorname { l o g } ^ { * } N )$ **time and** $O ( n \log ^ { * } N   +   w )$ **space.**

**Our data structures also solve the grammar compressed dictionary matching problem [15].**

Theorem 23. Given $a \mathit { \textsf { D S L P } } \left\langle \mathcal { S } , m \right\rangle$ of size n that represents a dictionary $\Pi _ { \langle \mathcal { S } , m \rangle }$ for m patterns of total length N, we can preprocess the DSLP in O((n log log n + m log m) log $\dot { N } \log ^ { * } N )$ time and O(n log $N \log ^ { * } N )$ space so that, given any text T in a streaming fashion, we can detect all occ occurrences of the patterns in $T$ **i**n $O ( | T |$ **log m log** $N \log ^ { * } N + o c c )$ **time.**

It was shown in [15] that we can construct in $O ( n ^ { 4 }$ log n) time a data structure of size $O ( n ^ { 2 } \log N )$ which finds all occurrences of the patterns in T in $O ( | T | ( h + m ) )$ **time, where** $h$ **is the height of the derivation tree of** $\mathrm { D S L P } \langle \mathcal { S } , m \rangle$ **. Note that our data structure of Theorem 23 is always smaller, and runs** faster when $h = \omega ( \operatorname { l o g } m \operatorname { l o g } N \operatorname { l o g } ^ { * } N )$

## 8 Appendix: Supplementary Examples and Figures

Example 24 $( \mathit { E b l o c k } _ { d } ( p )$ and Epow(s)). Let log∗ $W = 2 ,$ and then $\Delta _ { L } = 8 , \Delta _ { R } = 4 .$ $\mathit { I f } _ { } { p } = 1 , 2 , 3 , 2 , 5 , 7 , 6 , 4 , 3 , 4 , 3 , 4 , 1 , 2 , 3 , 4 , 5$ and $d = 1 , 0 , 0 , 1 , 0 , 1 , 0 , 0 , 1 , 0 , 0 , 0 , 1 , 0 , 1 , 0 , 0 )$ , then $\mathit { E b l o c k } _ { d } ( p ) =$ $( 1 , 2 , 3 ) , ( 2 , 5 ) , ( 7 , 6 , 4 ) , ( 3 , 4 , 3 , 4 ) , ( 1 , 2 ) , ( 3 , 4 , 5 ) , \left| \mathit { E b l o c k } _ { d } ( p ) \right| = 6 \mathit { a n d } \mathit { E b l o c k } _ { d } ( p ) [ 2 ] = ( 2 , 5 )$ **.** For string $s = a a b b b b b a b b ,  \mathit { E p o w } ( s ) = a ^ { 2 } b ^ { 5 } a ^ { 1 } b ^ { 2 }$ **a**nd $| \mathit { E p o w } ( s ) | = 4$ and $\mathit { E p o w } ( s ) [ 2 ] = b ^ { 5 }$

**Example 25 (SLP). Let** $\mathcal { S } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **be the SLP s.t.** $\Sigma \; = \; \{ A , B , C \} , \mathcal { V } \; = \; \{ X _ { 1 } , \cdots , X _ { 1 1 } \}$ $\mathcal { D } = \{ X _ { 1 } \to A , X _ { 2 } \to B , X _ { 3 } \to C , X _ { 4 } \to X _ { 3 } X _ { 1 } , X _ { 5 } \to X _ { 4 } X _ { 2 } , X _ { 6 } \to X _ { 5 } X _ { 5 } , X _ { 7 } \to X _ { 2 } X _ { 3 } , X _ { 8 } \to$ $X _ { 1 } X _ { 2 } , X _ { 9 } \to X _ { 7 } X _ { 8 } , X _ { 1 0 } \to X _ { 6 } X _ { 9 } , X _ { 1 1 } \to X _ { 1 0 } X _ { 6 } \} , S = X _ { 1 1 }$ **,** the derivation tree $o f ~ S$ **represents** $C A B C A B B C A B C A B C A B .$

10

<!-- page 11 of 17 -->

**Example 26 (RLSLP). Let** $\mathcal { G }   =   ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **be an RLSLP, where** $\Sigma   =   \{ A , B , C \} , \; \mathcal { V }   =   \{ 1 , \ldots , 1 5 \}$ $\mathcal{D}=\{1 \rightarrow A,2 \rightarrow B,3 \rightarrow C,4 \rightarrow 3^{4},5 \rightarrow 1^{1},6 \rightarrow 2^{1},7 \rightarrow 3^{1},8 \rightarrow (7,5),9 \rightarrow (8,6),10 \rightarrow (5,6),11 \rightarrow$ $(10,4),12 \rightarrow 9^{2},13 \rightarrow 10^{7},14 \rightarrow 11^{1},15 \rightarrow (12,13),16 \rightarrow (15,14),17 \rightarrow 16^{1} \}$ **,** and $S = 1 7$ . The derivation tree of the start symbol S represents a single string T = CABCABABABABABABABABABCCCC. Here, $\mathit { S i g } \big ( ( 7 , 5 ) \big )   =   8 , \; \mathit { S i g } \big ( ( 7 , 5 , 6 ) \big )   =   9 , \; \mathit { S i g } \big ( ( 6 , 5 ) \big )   =$ undefined. See also $F i g .$ **4** which illustrates the **derivation tree of the start symbol S and the DAG for G.**

**Example 27 (Signature encoding). Let** $\mathcal { G } \; = \; ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ **be an** $R L S L P$ **of Example 26. Assuming** $\mathit { E b l o c k } ( \mathit { P o w } _ { 0 } ^ { T } ) = ( 7 , 5 , 6 ) , ( 7 , 5 , 6 ) , ( 5 , 6 ) ^ { 7 } , ( 5 , 6 , 4 )$ and $\mathit { E b l o c k } ( \mathit { P o w } _ { 1 } ^ { T } ) = ( 1 2 , 1 3 , 1 4 )$ hold, G is the signa**ture encoding of T and i** $d ( T )   =   1 7$ **. See Fig. 4 for an illustration of the derivation tree of** $\mathcal { G }$ **and the corresponding DAG.**

![Image block](doc:d41ff6f/tier:standard/page:11/block:3)

Figure 4: The derivation tree of S (left) and the DAG for G (right) of Example 26. In the DAG, the black and red arrows represent $e \rightarrow e \ell e _ { i }$ r and $e   \rightarrow   \hat { e } ^ { k }$ respectively. In Example 27, T is encoded by signature encoding. In the derivation tree of $S ,$ the dotted boxes represent the blocks created by the Eblock function.

## 9 Appendix: Proof of Lemma 5

**Proof. Here we give only an intuitive description of a proof of Lemma 5. More detailed proofs can be found at [19] and [1].**

**Let p be an integer sequence of length n, called a W-colored sequence, where** $p [ i ] \neq p [ i + 1 ]$ **for any** $1 \leq i < n$ **and** $0 \leq p [ j ] \leq W$ **for any** $1 \leq j \leq n$ **. Mehlhorn et al. [19] showed that there exists a function** $f ^ { \prime }$ which returns a (log W)-colored sequence $p ^ { \prime }$ for a given W-colored sequence $p$ **in** $O ( | p | )$ **time, where** $p ^ { \prime } [ i ]$ <sub>is</sub> determined only by $p [ i   -   1 ]$ and p[i] for $1 \leq i \leq | p |$ . Let $p ^ { \langle k \rangle }$ denote the outputs after applying $f ^ { \prime }$ **to** p by k times. They also showed that there exists a function $f ^ { \prime \prime }$ which returns a bit sequence d satisfying the conditions of Lemma 5 for a 6-colored sequence p in $O ( | p | )$ time, where $d [ i ]$ is determined only by $p [ i - 3 . . i + 3 ]$ **f**or $1 \leq i \leq | p |$ **. Hence we can compute d for a W-colored sequence p in** $O ( | p | \log ^ { * } W )$ **time** by applying $f ^ { \prime \prime }$ to $p ^ { \langle \log ^ { * } \hat { W } \hat { + } 2 \rangle }$ after computing $\stackrel { \star } { p } ^ { \langle \log ^ { * } W + 2 \rangle }$ . Furthermore, Alstrup et al. [1] showed that d can be computed in $O ( | p | )$ time using a precomputed table of size o(log W). The idea is that $p ^ { \langle 3 \rangle }$ <strong><sub>i</sub></strong><sub>s</sub> a log log log $W \cdot$ colored sequence and the number of all combinations of a log log log W-colored sequence of length $\log ^ { * } W + 1 1$ **i**s $\stackrel { 1 } { 2 } ^ { ( \log ^ { * } W + 1 1 ) }$ **log log log** $\scriptstyle W \; = \; o ( \log W )$ **. Hence we can compute d for a W-colored sequence in linear time using a precomputed table of size o(log W).** □

## 10 Appendix: Omitted Proofs in Sections 4 and 5

## 10.1 Proof of Lemma 7

**Proof. Consider any integer i with** $T [ i . . i + | P | - 1 ] = P$ **(see also** $Fig. $5 ( 2 ) )$$ **. Note that for** $0 \leq t < h ^ { P }$ **, if** $\overset { \circ } { X } \overset { \circ } { S h r i n k _ { t } ^ { P } }$ occurs in $\tilde { { S h r i n k } _ { t } ^ { T } }$ , then $\dot { X } \dot { P } o w _ { t } ^ { P }$ always occurs in $P o w _ { t } ^ { T }$ , because $X { P } o w _ { t } ^ { P }$ **is determined only**

11

<!-- page 12 of 17 -->

(2)

![Image block](doc:d41ff6f/tier:standard/page:12/block:2)

![Image block](doc:d41ff6f/tier:standard/page:12/block:3)

![Image block](doc:d41ff6f/tier:standard/page:12/block:4)

Figure 5: Abstract images of consistent signatures of substring $P$ of text $T ,$ on the derivation trees of the signature encoding of $T .$ . Gray rectangles in Figures $(1)-(3)$ represent common signatures for occurrences of P. (1) Each $\tilde { \mathit { X S h r i n k } } _ { t } ^ { P }$ and $X P o w _ { t } ^ { \stackrel { \leftrightarrow } { P } }$ occur on substring $P$ in $s h r i n k _ { t } ^ { T }$ and $\stackrel { \circ } { { P o w } _ { t } ^ { T } }$ , respectively, where $T = L P R$ . (2) The substring P can be represented by $\hat { \tilde { L } } _ { 0 } ^ { P } L _ { 0 } ^ { P } \hat { L } _ { 1 } ^ { P } L _ { 1 } ^ { P } \hat { \mathit { X S h r i n k } } _ { 2 } ^ { P } R _ { 1 } ^ { P } \hat { \tilde { R } } _ { 1 } ^ { P } R _ { 0 } ^ { \tilde { P } } \hat { R } _ { 0 } ^ { P }$ . (3) There exist common signatures on every substring P in the derivation tree.

by $\mathit { X S h r i n k } _ { t } ^ { P }$ . Similarly, for $0 < t \leq h ^ { P }$ , if $X P o w _ { t - 1 } ^ { P }$ occurs in $P o w _ { t - 1 } ^ { T } ,$ , then $X \mathit { S h r i n k } _ { t } ^ { P }$ **always occurs in** $S h r i n k _ { t } ^ { T }$ . Since $X \mathit { S h r i n k } _ { 0 } ^ { P }$ occurs at position i in $S h r i n k _ { 0 } ^ { T }$ **, XShrink P**and $X P o w _ { t } ^ { P }$ occur in the derivation tree of $i d ( T )$ . Hence we discuss the positions of $X \overset { \circ } { { S h r i n k } _ { t } ^ { P } }$ and $\stackrel{\circ}{X}POw_{t}^{P}$ . Now, let $\hat { c } _ { t }   +   1$ and $c _ { t }   +   1$ be the beginning positions of the corresponding occurrence of $\mathit { X S h r i n k } _ { t } ^ { P }$ in $\tilde { { S h r i n k } _ { t } ^ { T } }$ and that of $X P o w _ { t } ^ { P }$ in $P o w _ { t } ^ { T }$ **,** respectively. Then $\hat { \mathit { S h r i n k } } _ { t } ^ { T } [ \hat { . . . } \hat { c } _ { t } ]$ consists of ${ P o w } _ { t - 1 } ^ { T } [ . . c _ { t - 1 } ]$ and $L _ { t - 1 } ^ { P }$ for $0   <   t   \leq   h ^ { P }$ . Also, ${ P o w } _ { t } ^ { T } [ . . c _ { t } ]$ consists of $\mathit { S h r i n k } _ { t } ^ { T } [ . . \hat { c } _ { t } ]$ and $\hat { L } _ { t } ^ { P }$ for $0 \leq t < h ^ { P }$ **. This means that the substring** $P$ **occurring at position i in** $T$ **is represented as** $U n i q ( P )$ **in the signature encoding Therefore Lemma 7 holds.**

## 10.2 Proof of Lemma 9

**Proof. By Definition** $^ { 8 , }$ **for every level, X contains** $O ( \log ^ { * } M )$ **nodes** that are parents of the nodes **representing** $U n i q ( P )$ **. Lemma 9 holds because the number of nodes at some level is halved when Shrink is applied. More precisely, considering the x nodes of X at some level to which Shrink is applied, the number of their parents is at most** $( x + 2 ) / 2$ **. Here the** $^ { \mathfrak { c } } + 2 ^ { \mathfrak { z } }$ **term reflects the fact that both ends of** x nodes may be coupled with nodes outside X. And also, since $| \mathit { E p o w } ( \hat { L } _ { t } ^ { P } ) |   =   | \mathit { E p o w } ( \hat { R } _ { t } ^ { P } ) |   =   1$ **for** $0 \leq t < h ^ { P }$ and $| \bar { \mathit { E p o w } } ( \mathit { X S h r i n k } _ { h ^ { P } } ^ { P } ) | = O ( | \operatorname { l o g } ^ { * } M | )$ , each nodes representing $\left( \hat { \bar { L } } _ { t } ^ { \dot { P } } \right.$ and $\widehat { R } _ { t } ^ { P }$ **has a common** parent for every level, and the number of parents of nodes representing $\bar { X } \bar { S } \bar { h } r i n k _ { h ^ { P } } ^ { P }$ **is** $O ( \log ^ { * } M )$ **. Note that** $h = O ( \log | v a l ( e ) | )$ **holds for** $e \in \mathcal { V }$ **by the signature encoding, where h is the height of derivation tree of e.** □

12

<!-- page 13 of 17 -->

## 10.3 Proof of Lemma 11

**Proof. Let T be the derivation tree of e and consider the induced subtree X of T whose root is the root of T and whose leaves are the parents of the nodes representing** $\mathit { U n i q } ( s [ j . . j \: + \: y \: - \: 1 ] )$ **. Then the size of X is** $O ( \log | s |$ **+ log** $y \log ^ { * } M )$ **by Lemma 9. Starting at the given node in the DAG which corresponds to e, we compute** $X$ **using Definition 8 and the properties described in the proof of Lemma 9 in** $O ( \operatorname { l o g } | s | + \operatorname { l o g } y \operatorname { l o g } ^ { * } M )$ **time. Hence Lemma 11 holds.** □

## 11 Appendix: Omitted Proofs in Section 6

## 11.1 Proof of Theorem 3 (2)

**Proof. Consider a dynamic signature encoding G for an empty string. Then Theorem** $3 ~ ( 2 )$ **immediately** holds by computing $\mathit { I N S E R T } ^ { \prime } ( c _ { i } , | f _ { i } | , | f _ { 1 } \cdots f _ { i - 1 } | { + } 1 )$ for all $1 \leq i \leq z$ **i**ncrementally, where $c _ { i } \leq$ $| f _ { 1 } \cdots f _ { i - 1 } | { - } | f _ { i } |$ **i**s a position such that $T [ c _ { i } . . c _ { i } { + } | f _ { i } | { - } 1 ] = f _ { i }$ holds. Note that when $f _ { i }$ **i**s a character which does not occur in $f _ { 1 } , \ldots f _ { i - 1 }$ for $1 \leq i \leq z$ , we compute $\mathit { I N S E R T } ( f _ { i } , | f _ { 1 } \cdots f _ { i - 1 } | { + } 1 )$ **i**n $O ( f _ { \mathcal { A } } \operatorname { l o g } N \operatorname { l o g } ^ { * } M )$ time instead of the above $\mathit { I N S E R T ^ { \prime } }$ **operation.** □

**Note that we can directly show Lemma 6 from the above proof because the size of G increases** $O ( \operatorname { l o g } N \operatorname { l o g } ^ { * } M )$ by Lemma 9, every time we do $\begin{aligned} { \mathit { I N S E R T } ^ { \prime } ( c _ { i } , | f _ { i } | , | f _ { 1 } \cdots f _ { i - 1 } | + 1 ) } \\ \end{aligned}$ for $1 \leq i \leq z$

## 11.2 Proof of Theorem 3 (3a)

Proof. We use the G-factorization proposed in [22]. By the G-factorization of T with respect to $S ,   T$ is partitioned into $O ( n )$ **strings, each of which, corresponding to** $T [ i . . j ]$ **, is derived by a variable X of** $\mathcal { S }$ **such that X appears in the derivation tree of S to derive a substring of** $T [ 1 . . i - 1 ]$ **, or otherwise X derives a single character that does not appear in** $T [ 1 . . i - 1 ]$ **. Note that we can compute a sequence of variables of** $\mathcal { S }$ **corresponding to the G-factorization of** $T$ **with respect to** $\mathcal { S }$ **in** $O ( n )$ **t**ime by the depth-first **traversal of the DAG of S. Since the G-factorization resembles the LZ77 factorization, we can construct the dynamic signature encoding** $\mathcal { G }$ **f**or $T$ by $O ( n )$ INSERT′ and $I N S E R T$ operations as the proof of **Theorem 3 (2).** □

## 11.3 Proof of Lemma 15

Proof. We first compute, for all variables $X _ { i } , \; \mathit { E p o w } ( \mathit { X S h r i n k } _ { t } ^ { X _ { i } } ) \; \mathit { i f } \; | \mathit { E p o w } ( \mathit { X S h r i n k } _ { t } ^ { X _ { i } } ) | \leq \Delta _ { L } + \Delta _ { R } + 9 ,$ otherwise $\mathit { E p o w } ( \hat { L } _ { t } ^ { \hat { X _ { i } } } )$ and $\mathit { E p o w } ( \hat { R } _ { t } ^ { X _ { i } } )$ The information can be computed in $O ( n \log ^ { * } M )$ time and space in a bottom-up manner, $\mathrm { i . e . } ,$ by processing variables in increasing order. For $X _ { i } \to X _ { \ell } X _ { r } ,$ **i**f both $| \mathit { E p o w } ( \bar { \mathit { X S h r i n k } _ { t } ^ { X _ { \ell } } } ) |$ and $| \vec { \mathit { E p o w } ( \mathit { X S h r i n k } _ { t } ^ { \mathit { X } _ { r } } ) } |$ are no greater than $\Delta _ { L } + \Delta _ { R } + 9$ , we can compute $\dot { \mathit { E p o w } } ( \dot { \mathit { X S h r i n k } } _ { t } ^ { X _ { i } } )$ **i**n $O ( \log ^ { * } M )$ time by naively concatenating $\mathit { E p o w } ( \mathit { X S h r i n k } _ { t } ^ { X _ { \ell } } )$ **,** $\dot { \mathit { E p o w } } ( \hat { z } _ { t _ { \cdot } } ^ { X _ { i } } )$ and Epow(XShrink<sup>Xr</sup>t ). Otherwise $| \mathit { E p o w } ( \mathit { X S h r i n k } _ { t } ^ { X _ { i } } ) | \: > \: \Delta _ { L } \: + \: \Delta _ { R } \: + \: 9$ must hold, and $\mathit { E p o w } ( \hat { L } _ { 0 } ^ { X _ { i } } )$ **and** $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { i } } )$ can be computed in $O ( 1 )$ time from $\mathit { E p o w } ( \hat { z } _ { t _ { \tau \tau } } ^ { X _ { i } } )$ and the information for $X _ { \ell }$ **and** $X _ { r }$

The run-length encoded signatures represented $\widehat{\mathrm{by}_{..}z_{t}^{X_{i}}}$ can be obtained in $O ( \log ^ { * } M )$ **t**ime by using $\hat { z } _ { t } ^ { X _ { i } }$ and the above information for $X _ { \ell }$ and $X _ { r } \colon \dot { z _ { t } ^ { X _ { i } } }$ **i**s created over run-length encoded signatures that are obtained by concatenating $\mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { \ell } } )$ (or $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { \ell } } ) ) , \: z _ { t } ^ { X _ { i } }$ and $\mathit { E p o w } ( \mathit { X S h r i n k } _ { 0 } ^ { X _ { r } } )$ (or $\mathit { E p o w } ( \hat { R } _ { 0 } ^ { X _ { r } } ) )$ . Also, $A _ { t } ^ { X _ { n } }$ **and** $B _ { t } ^ { X _ { n } }$ **r**epresents $\hat { A } _ { t } ^ { X _ { n } } \hat { \bar { L } } _ { t } ^ { X _ { n } ^ { \top } }$ and $\hat { R } _ { t } ^ { X _ { n } } \hat { B } _ { t } ^ { \hat { X _ { n } } }$ , respectively.

**Hence, we can compute in** $O ( n \log ^ { * } M )$ **time** $O ( n \log ^ { * } M )$ **run-length encoded signatures to which we give signatures. We determine signatures in** $O ( n \operatorname { l o g } \operatorname { l o g } ( n \operatorname { l o g } ^ { * } M ) \operatorname { l o g } ^ { * } M )$ **time by sorting the run-length encoded signatures as Lemma 15.** □

## Appendix D: Omitted Proofs in Section 7

## 11.4 Proof of Theorem 17

## 11.4.1 Proof of Theorem 17 (1)

**Proof. For any signature** $e \in \mathcal { V }$ **s**uch that $e \rightarrow e _ { \ell } e _ { r }$ , we can easily translate e to a production of $\mathrm { S L P s }$ **because the assignment is a pair of signatures, like the right-hand side of the production rules of SLPs.** For any signature $e \in \mathcal { V }$ such that $e \to \hat { e } ^ { k }$ **, we can translate e to at most 2 log k production rules of SLPs:**

13

<!-- page 14 of 17 -->

We create $t = \lfloor \log k \rfloor$ variables which represent $\hat { e } ^ { 2 ^ { 1 } } , \hat { e } ^ { 2 ^ { 2 } } , \ldots , \hat { e } ^ { 2 ^ { t } }$ and concatenating them according to the binary representation of k to make up $k ~ { \hat { e } } ^ { \dagger } \mathrm { s } .$ Therefore we can compute $\mathcal { S }$ in $O ( w \log | T | )$ **t**ime. □

## 11.4.2 Proof of Theorem 17 (2)

Proof. Note that the number of created or removed signatures in V is bounded by $O ( y + \log | T ^ { \prime } | \log ^ { * } M )$ by Lemma 9. For each of the removed signatures, we remove the corresponding production from $\mathcal { S } .$ **For each of created signatures, we create the corresponding production and add it to** $\mathcal { S }$ **as in the proof of (1). Therefore Theorem 17 holds.** □

## 11.5 Proof of Theorem 20

**We use the following known result.**

**Lemma 28 ([1]). Using signature encodings** $\mathcal { G } _ { 1 } , \ldots \mathcal { G } _ { m }$ **, we can support**

$\mathit { L C P } ( T _ { i } , T _ { j } ) \mathit { i n } O ( \operatorname { l o g } | T _ { i } | + \operatorname { l o g } | T _ { j } | )$ **time,**

$\mathit { L C S } ( T _ { i } , T _ { j } )$ **i**n $O ( ( \operatorname { l o g } | T _ { i } | + \operatorname { l o g } | T _ { j } | ) \operatorname { l o g } ^ { * } M )$ **time**

where $T _ { i } , T _ { j } \in \{ T _ { 1 } , \ldots , T _ { m } \}$ and $\mathcal { G } _ { k }   =   ( \Sigma , \mathcal { V } , \mathcal { D } , S _ { k } )$ of a string $T _ { k }$ for $1   \leq   k   \leq   m$ , namely $\mathcal { G } _ { 1 } , \ldots , \mathcal { G } _ { m }$ **share D.**

Proof. We compute $\mathit { L C P } ( T _ { i } , T _ { j } )$ by $\mathit { L C E } ( T _ { i } , T _ { j } , 1 , 1 )$ , namely, we use the algorithm of Lemma 10. Let P denote the longest common prefix of $T _ { i }$ and $T _ { j }$ . We use the notation $\widehat { A } ^ { P }$ defined in Section 6.3. Then the both substrings $P$ occurring at position 1 in $T _ { i }$ and at position 1 in $T _ { j }$ are represented as $v = \hat { A } _ { h ^ { P } } ^ { P } \mathit { X S h r i n k } _ { h ^ { P } } ^ { P } R _ { h ^ { P } - 1 } ^ { P } \hat { R } _ { h ^ { P } - 1 } ^ { P } \cdots R _ { 0 } ^ { P } \hat { R } _ { 0 } ^ { P }$ **i**n the signature encoding by a similar argument of Lemma 7. Since $| \hat { \mathit { E p o w } } ( v ) | = O ( \log | P | + \log ^ { * } M )$ , we can compute $\mathit { L C P } ( T _ { i } , T _ { j } )$ **i**n $O ( \operatorname { l o g } | T _ { i } | + \operatorname { l o g } | T _ { j } | )$ **t**ime. Similarly, we can compute $\mathit { L C S } ( T _ { i } , T _ { j } )$ **i**n $O ( ( \operatorname { l o g } | T _ { i } | + \operatorname { l o g } | T _ { j } | ) \operatorname { l o g } ^ { * } M )$ **time. More detailed proofs can be found in [1].** □

**To use Lemma 28 for** $\mathit { i d } ( \mathit { v a l } ( X _ { 1 } ) ) , \dots , \mathit { i d } ( \mathit { v a l } ( X _ { n } ) )$ **, we show the following lemma.**

**Lemma 29. Given an** ${ S L P } \; \mathcal { S } ,$ **we can compute** $\mathit { i d } ( \mathit { v a l } ( X _ { 1 } ) ) , \dots , \mathit { i d } ( \mathit { v a l } ( X _ { n } ) )$ **in**

**O(n log log n log** $N \log ^ { * } M )$ **time and O(n log N log**<strong><sup>∗</sup></strong> **M) space.**

Proof. Recall that the algorithm of Theorem 3 (3) computes $i d ( v a l ( X _ { n } ) )$ in O(n log log n log $N \log ^ { * } M )$ **time**. We can modify the algorithm to compute $\mathit { i d } ( \mathit { v a l } ( X _ { 1 } ) ) , \dots , \mathit { i d } ( \mathit { v a l } ( X _ { n } ) )$ without changing the time complexity: We just compute $A _ { t } ^ { X } ,   \hat { A } _ { t } ^ { X } ,   B _ { t } ^ { \hat { X } }$ and $\hat { B } _ { t } ^ { X }$ for $``all   X \in \mathcal{S},$ **not only for** $X _ { n }$ **. Since the total size is O(n log** $N \log ^ { * } M )$ **, Lemma 29 holds.** □

**We are ready to prove Theorem 20.**

**Proof. The first result immediately follows from Lemma 28 and 29. To speed-up query times for** LCP **and** LCS**, we sort variables in lexicographical order in** $O ( n$ **log n log** $N )$ **time by** LCP **query and a standard comparison-based sorting. Constant-time** LCP **queries are then possible by using a constant-time RMQ data structure [4] on the sequence of the lcp values. Next we show that** LCS **queries can be supported** similarly. Let SLP $\mathcal { S } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ and $Y _ { i } \rightarrow e x p r _ { i }$ for $1 \leq i \leq n$ **, where** exp $r _ { i } = Y _ { r } Y _ { \ell }$ for $X _ { i } \to X _ { \ell } X _ { r } \in$ $\mathcal { D }$ and $\mathit { e x p r } _ { i }   =   a$ fo**r** $( X _ { i }   \to   a   \in   \Sigma )   \in   \mathcal { D } .$ **T**hen consider an SLP $\mathcal { S } ^ { \prime }   =   ( \dot { \Sigma , \mathcal { V } ^ { \prime } } , \mathcal { D } , S ^ { \prime } )$ of size $n ,$ where $\mathcal { V } ^ { \prime } = \{ Y _ { 1 } , \ldots , Y _ { n } \} , \mathcal { D } ^ { \prime } = \{ Y _ { 1 } \to \mathit { e x p r } _ { i } , \ldots , Y _ { n } \to \mathit { e x p r } _ { n } \}$ and $S ^ { \prime } = Y _ { n }$ . Namely $S ^ { \prime }$ represents $T ^ { R }$ **. By supporting** LCP queries on $S ^ { \prime }$ , LCS queries on $\mathcal { S }$ **can be supported. Hence Theorem 20 holds.** □

## 11.6 Proof of Theorem 21

$$
\left. N \log^ {*} M\right)
$$

Proof. We can compute a static signature encoding $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ of size w representing $T$ in $O ( n$ log log n lo **time and** $O ( n \log ^ { * } M + w )$ **working space using Theorem** $^ { 3 , }$ **where** $w = O ( z \log N \log ^ { * } M )$ **. Notice that** each variable of the SLP appears at least once in the derivation tree of $T _ { n }$ of the last variable $X _ { n }$ rep**resenting the string T . Hence, if we store an occurrence of each variable** $X _ { i }$ **i**n $\mathcal { T } _ { n }$ **and** $| v a l ( X _ { i } ) |$ **, we can reduce any LCE query on two variables to an LCE query on two positions of** $v a l ( X _ { n } )   =   T$ **. In so doing, for all** $1 \leq i \leq n$ **we first compute** $| v a l ( X _ { i } ) |$ **and then compute the leftmost occurrence** $\ell _ { i }$ **of** $X _ { i }$ **i**n $\mathcal { T } _ { n }$ **, spending** $O ( n )$ **total time and space. By Lemma 10, each LCE query can be supported in** O(log $N   +   \log \ell \log ^ { * } M )$ time. Since $z \leq n [ 2 2 ]$ , the total preprocessing time is $O ( n$ log log n log $N \log ^ { * } M )$ and working space is $O ( n \log ^ { * } M + w )$ □

14

<!-- page 15 of 17 -->

## 11.7 Proof of Theorem 22

Proof. For a given SLP of size n representing a string of length $N .$ let $P ( n , N ) ,   S ( n , N )$ , and $E ( n , N )$ **be the preprocessing time and space requirement for an** LCE **data structure on SLP variables, and each** LCE **query time, respectively.**

**Matsubara et al. [18] showed that we can compute an** $O ( n$ **log** $N )$ **-size representation of all palindromes in the string in** $O ( \widetilde { P ( n , N ) } + E ( n , N ) \cdot \mathit { n } \operatorname { l o g } N )$ **time and O(n log** $N + S ( n , N ) )$ **space. Hence, using** Theorem 21, we can find all palindromes in the string in O(n log log n log N log $M + n \log ^ { 2 } N \log ^ { * } M ) =$ $O ( n \operatorname { l o g } ^ { 2 } N \operatorname { l o g } ^ { * } M )$ **time and** $O ( n \log ^ { * } M + w )$ **space.** □

## 11.8 Proof of Theorem 23

Proof. In the preprocessing phase, we construct a static signature encoding $\mathcal { G } = ( \Sigma , \mathcal { V } , \mathcal { D } , S )$ of size $w ^ { \prime }$ such that id $\mathit { l } ( \mathit { v a l } ( X _ { 1 } ) ) , \dots , \mathit { i d } ( \mathit { v a l } ( X _ { n } ) ) \in \mathcal { V }$ **using Lemma 29, spending O(n log log n** log $N \log ^ { * } M )$ time, where $w ^ { \prime }   =   O ( n \log N \log ^ { * } M )$ . Next we construct a compacted trie of size $O ( m )$ **that represents the m patterns, which we denote by PTree (pattern tree). Formally, each non-root node of PTree represents either a pattern or the longest common prefix of some pair of patterns.** $P T$ **ree can be built by using** LCP **of Theorem 20 in** $O ( m$ **log m log N) time. We let each node have its string depth, and the pointer to its deepest ancestor node that represents a pattern if such exists. Further, we augment PTree with a data structure for level ancestor queries so that we can locate any prefix of any pattern, designated by a pattern and length, in** $P T r e e$ **in** $O ( \log m )$ **time by locating the string depth by binary search on the path from the root to the node representing the pattern. Supposing that we know the longest prefix of** $T [ i . . | T ]$ **that is also a prefix of one of the patterns, which we call the max-prefix for i, PTree allows us to output occ**<strong><sub>i</sub></strong> **patterns occurring at position i in** $O ( \log m + o c c _ { i } )$ **time. Hence, the pattern matching problem reduces to computing the max-prefix for every position.**

**In the pattern matching phase, our algorithm processes** $T$ **in a streaming fashion, i.e., each character is processed in increasing order and discarded before taking the next character. Just before processing** $T [ j + 1 ]$ **, the algorithm maintains a pair of signature** $p$ **and integer l such that** $v a l ( p ) [ 1 . . l ]$ **is the longest suffix of** $T [ 1 . . j ]$ **that is also a prefix of one of the patterns. When** $T [ j + 1 ]$ **comes, we search for the smallest position** $i \in \{ j - l + 1 , \ldots , j + 1 \}$ **such that there is a pattern whose prefix is** $T [ i . . j + 1 ]$ **. For each** $i \in \{ j - l + 1 , \ldots , j + 1 \}$ in increasing order, we check if there exists a pattern whose prefix is $T [ i . . j + 1 ]$ by binary search on a sorted list of m patterns. Since $T [ i . . j ]   =   v a l ( p ) [ i - j + l . . l ]$ **,** LCE **with** $p$ **can be used for comparing a pattern prefix and** $T [ i . . j + 1 ]$ **(except for the last character** $T [ j + 1 ] )$ **,** and hence, **the binary search is conducted in O(log m log** $N \log ^ { * } M )$ **time. For each** $i ,$ **if there is no pattern whose prefix is** $T [ i . . j + 1 ]$ **, we actually have computed the max-prefix for** $i ,$ **and then we output the occurrences of patterns at i. The time complexity is dominated by the binary search, which takes place** $O ( | T | )$ **times in total. Therefore, the algorithm runs in** $O ( | T |$ **log m log** $N \log ^ { * } M + o c c )$ **time.**

**By the way, one might want to know occurrences of patterns as soon as they appear as Aho-Corasick automata do it by reporting the occurrences of the patterns by their ending positions. Our algorithm described above can be modified to support it without changing the time and space complexities. In the preprocessing phase, we additionally compute RPTree (reversed pattern tree), which is analogue to** PTree but defined on the reversed strings of the patterns, i.e., RPTree is the compacted trie of size $O ( m )$ that represents the reversed strings of the m patterns. Le**t** $T [ i . . j ]$ **be the longest suffix of** $T [ 1 . . j ]$ **that is** also a prefix of one of the patterns. $\mathrm { A }$ suffix $T [ i ^ { \prime } . . j ]$ **of** $T [ i . . j ]$ **is called the max-suffix for** $j$ **iff it is the longest suffix of** $T [ i . . j ]$ **that is also a suffix of one of the patterns. Supposing that we know the max-suffix** for $j ,$ RPTree allows us to output eocc<sub>j</sub> patterns occurring with ending position $j$ in $O ( \log m + e o c c _ { j } )$ time. Given a pair of signature $p$ and integer l such that $T [ i . . j ] \: = \: v a l ( p ) [ 1 . . l ]$ , the max-suffix for $j$ can be computed in $O ( \log$ **m log** $N \log ^ { * } M )$ **time by binary search on a list of m patterns sorted by their “reversed” strings since each comparison can be done by “leftward”** LCE **with** $p .$ **Except that we compute the max-suffix for every position and output the patterns ending at each position, everything else is the same as the previous algorithm, and hence, the time and space complexities are not changed.** □

15

<!-- page 16 of 17 -->

## References

[1] Stephen Alstrup, Gerth Stølting Brodal, and Theis Rauhe. Dynamic pattern matching. Technical report, Department of Computer Science, University of Copenhagen, 1998.

[2] Stephen Alstrup, Gerth Stølting Brodal, and Theis Rauhe. Pattern matching in dynamic texts. In Proc. SODA 2000, pages 819–828, 2000.

[3] Paul Beame and Faith E. Fich. Optimal bounds for the predecessor problem and related problems. J. Comput. Syst. Sci., 65(1):38–72, 2002.

[4] M. A. Bender, M. Farach-Colton, G. Pemmasani, S. Skiena, and P. Sumazin. Lowest common ancestors in trees and directed acyclic graphs. J. Algorithms, 57(2):75–94, 2005.

[5] P. Bille, P. H. Cording, I. L. Gørtz, B. Sach, H. W. Vildhøj, and Søren Vind. Fingerprints in compressed strings. In Proc. WADS 2013, pages 146–157, 2013.

[6] Philip Bille, Anders Roy Christiansen, Patrick Hagge Cording, and Inge Li Gørtz. Finger search, random access, and longest common extensions in grammar-compressed strings. CoRR, abs/1507.02853, 2015.

[7] Philip Bille, Inge Li Gørtz, Mathias Bæk Tejs Knudsen, Moshe Lewenstein, and Hjalte Wedel Vildhøj. Longest common extensions in sublinear space. In Ferdinando Cicalese, Ely Porat, and Ugo Vaccaro, editors, Combinatorial Pattern Matching - 26th Annual Symposium, CPM 2015, Ischia Island, Italy, June 29 - July 1, 2015, Proceedings, volume 9133 of Lecture Notes in Computer Science, pages 65–76. Springer, 2015.

[8] Francisco Claude and Gonzalo Navarro. Self-indexed grammar-based compression. Fundamenta Informaticae, 111(3):313–337, 2011.

[9] Johannes Fischer, Tomohiro I, and Dominik Köppl. Deterministic sparse suffix sorting on rewritable texts. In LATIN 2016: Theoretical Informatics - 12th Latin American Symposium, Ensenada, Mexico, April 11-15, 2016, Proceedings, pages 483–496, 2016.

[10] Pawel Gawrychowski, Adam Karczmarz, Tomasz Kociumaka, Jakub Lacki, and Piotr Sankowski. Optimal dynamic strings. CoRR, abs/1511.02612, 2015.

[11] Dan Gusfield. Algorithms on Strings, Trees, and Sequences. Cambridge University Press, 1997.

[12] Yijie Han. Deterministic sorting in O(n log log n) time and linear space. Proc. STOC 2002, pages 602–608, 2002.

[13] Tomohiro I, Wataru Matsubara, Kouji Shimohira, Shunsuke Inenaga, Hideo Bannai, Masayuki Takeda, Kazuyuki Narisawa, and Ayumi Shinohara. Detecting regularities on grammar-compressed strings. Inf. Comput., 240:74–89, 2015.

[14] Tomohiro I, Yuto Nakashima, Shunsuke Inenaga, Hideo Bannai, and Masayuki Takeda. Faster Lyndon factorization algorithms for SLP and LZ78 compressed text. Theoretical Computer Science, 2016. in press.

[15] Tomohiro I, Takaaki Nishimoto, Shunsuke Inenaga, Hideo Bannai, and Masayuki Takeda. Compressed automata for dictionary matching. Theor. Comput. Sci., 578:30–41, 2015.

[16] Yury Lifshits. Processing compressed texts: A tractability border. In Proc. CPM 2007, volume 4580 of LNCS, pages 228–240, 2007.

[17] S. Maruyama, M. Nakahara, N. Kishiue, and H. Sakamoto. ESP-index: A compressed index based on edit-sensitive parsing. J. Discrete Algorithms, 18:100–112, 2013.

[18] W. Matsubara, S. Inenaga, A. Ishino, A. Shinohara, T. Nakamura, and K. Hashimoto. Efficient algorithms to compute compressed longest common substrings and compressed palindromes. Theor. Comput. Sci., 410(8–10):900–913, 2009.

[19] Kurt Mehlhorn, R. Sundar, and Christian Uhrig. Maintaining dynamic sequences under equality tests in polylogarithmic time. Algorithmica, 17(2):183–198, 1997.

16

<!-- page 17 of 17 -->

[20] M. Miyazaki, A. Shinohara, and M. Takeda. An improved pattern matching algorithm for strings in terms of straight-line programs. In Proc. CPM 1997, pages 1–11, 1997.

[21] Takaaki Nishimoto, Tomohiro I, Shunsuke Inenaga, Hideo Bannai, and Masayuki Takeda. Fully dynamic data structure for LCE queries in compressed space. CoRR, abs/1605.01488, 2016.

[22] Wojciech Rytter. Application of Lempel-Ziv factorization to the approximation of grammar-based compression. Theoretical Computer Science, 302(1–3):211–222, 2003.

[23] S. C Sahinalp and Uzi Vishkin. Data compression using locally consistent parsing. TechnicM report, University of Maryland Department of Computer Science, 1995.

[24] Yuka Tanimura, Tomohiro I, Hideo Bannai, Shunsuke Inenaga, Simon J. Puglisi, and Masayuki Takeda. Deterministic sub-linear space LCE data structures with efficient construction. In Proc. CPM 2016, 2016. to appear.

[25] J. Ziv and A. Lempel. A universal algorithm for sequential data compression. IEEE Transactions on Information Theory, IT-23(3):337–349, 1977.

17